package arlo

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// These tests run Run against fakeArlo inside synctest bubbles: time only
// moves when every goroutine waits, so pings, backoffs and renewals take no
// real time.

// connected is what Run reports on each connection to fakeArlo.
var connected = []string{
	"devices B C1 C2",
	"up",
	"state B connected=true battery=-", // first ping
	"mode L1 Home standby",
	// B's answer to "get devices", on the Event stream
	"state C1 connected=true battery=80", "motion C1 false",
	"state C2 connected=true battery=45", "motion C2 false",
}

type testRun struct {
	t      *testing.T
	c      *Client
	mu     sync.Mutex
	events []string
	done   chan struct{}
	err    error // Run's, once done is closed
}

// startRun starts Run on a fresh Session against fake, over an in-memory
// network. code nil means the account cannot do two-factor.
func startRun(t *testing.T, fake *fakeArlo, code CodeFunc) *testRun {
	l := newPipeListener()
	srv := &http.Server{Handler: fake}
	go func() { _ = srv.Serve(l) }()
	cfg := Config{
		Email: "me@example.com", Password: "secret", Code: code,
		SessionPath: filepath.Join(t.TempDir(), "session.json"),
		Log:         slog.New(slog.DiscardHandler),
	}
	a := newAPI(cfg, "http://arlo.test", "http://arlo.test")
	a.http.SetDial(l.dial)
	r := &testRun{t: t, c: newClient(cfg, a, fake.dial), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		r.err = r.c.Run(ctx, func(e Event) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, describe(e))
		})
		close(r.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-r.done
		_ = srv.Close()
	})
	return r
}

func withCode(context.Context, time.Time) (string, error) { return "123456", nil }

// take returns what Run reported since the last take, once it is idle.
func (r *testRun) take() []string {
	synctest.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	es := r.events
	r.events = nil
	return es
}

func (r *testRun) expect(step string, want ...string) {
	r.t.Helper()
	if got := r.take(); !slices.Equal(got, want) {
		r.t.Errorf("%s:\n got %q\nwant %q", step, got, want)
	}
}

func TestRunEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeArlo(t)
		r := startRun(t, fake, withCode)
		r.expect("connect", connected...)
		want := []string{"d/XB/out/cameras/#", "d/XB/out/devices/#", "u/U1/in/userSession/connect", "u/U1/in/userSession/disconnect"}
		if !slices.Equal(fake.topics, want) {
			t.Errorf("topics %q", fake.topics)
		}

		fake.send(`{"action":"is","from":"B","properties":{"motionDetected":true},"resource":"cameras/C1"}`)
		r.expect("motion", "motion C1 true")

		// A base is present while it answers pings, every minute.
		time.Sleep(time.Minute)
		r.expect("ping, no change")
		fake.set(func(f *fakeArlo) { f.pingStatus = http.StatusInternalServerError })
		time.Sleep(time.Minute)
		r.expect("ping failed", "state B connected=false battery=-")
		fake.set(func(f *fakeArlo) { f.pingStatus = 0 })
		time.Sleep(time.Minute)
		r.expect("ping back", "state B connected=true battery=-")
	})
}

func TestRunReconnects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause func(*fakeArlo)
		after time.Duration // until Run notices
		auth  bool          // whether reconnecting authenticates
	}{
		{"MQTT drop", (*fakeArlo).drop, 0, false},
		{"logout", func(f *fakeArlo) { f.send(`{"action":"logout"}`) }, 0, false},
		// Arlo voids the token: the next ping gets a 401.
		{"401 ping", func(f *fakeArlo) { f.set(func(f *fakeArlo) { f.issued = "void" }) }, time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fake := newFakeArlo(t)
				r := startRun(t, fake, withCode)
				r.expect("connect", connected...)
				tc.cause(fake)
				time.Sleep(tc.after)
				r.expect("cause", "down")
				fake.takeCalls()

				time.Sleep(time.Minute - time.Second)
				r.expect("backoff")
				time.Sleep(time.Second)
				r.expect("reconnect", connected...)
				if calls := fake.takeCalls(); slices.Contains(calls, "/api/auth") != tc.auth {
					t.Errorf("calls %q", calls)
				}
			})
		})
	}
}

// Arlo's auth rate limit has a long cooldown: a refused auth waits an hour.
func TestRunWaitsOutRefusedAuth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeArlo(t)
		r := startRun(t, fake, withCode)
		r.expect("connect", connected...)
		fake.set(func(f *fakeArlo) { f.issued, f.refuse = "void", true })
		time.Sleep(time.Minute)
		r.expect("401 ping", "down")
		fake.takeCalls()

		time.Sleep(time.Minute)
		synctest.Wait()
		if calls := fake.takeCalls(); !slices.Equal(calls, []string{"/api/validateAccessToken", "/api/auth"}) {
			t.Errorf("refused: %q", calls)
		}
		fake.set(func(f *fakeArlo) { f.refuse = false })
		time.Sleep(time.Hour - time.Second)
		synctest.Wait()
		if calls := fake.takeCalls(); calls != nil {
			t.Errorf("retried within the hour: %q", calls)
		}
		time.Sleep(time.Second)
		r.expect("reconnect", connected...)
	})
}

func TestRunNeedsCode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startRun(t, newFakeArlo(t), nil)
		<-r.done
		if !errors.Is(r.err, errNeedsCode) {
			t.Errorf("Run: %v", r.err)
		}
		r.expect("events")
	})
}

// Tokens last two hours: Run replaces them ten minutes before, unseen.
func TestRunRenewsSilently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeArlo(t)
		r := startRun(t, fake, withCode)
		r.expect("connect", connected...)
		time.Sleep(110*time.Minute - time.Second)
		r.take()
		fake.takeCalls()

		time.Sleep(time.Second)
		es := r.take()
		if slices.Contains(es, "down") || slices.Contains(es, "up") || !slices.Contains(es, "devices B C1 C2") {
			t.Errorf("renewal events %q", es)
		}
		if calls := fake.takeCalls(); !slices.Contains(calls, "/api/auth") {
			t.Errorf("renewal calls %q", calls)
		}
	})
}

func TestRunCommands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeArlo(t)
		r := startRun(t, fake, withCode)
		r.expect("connect", connected...)
		ctx := t.Context()

		if err := r.c.SetMode(ctx, ArmAway); err != nil {
			t.Fatal(err)
		}
		// Reported once set, then again as B applies it.
		r.expect("set mode", "mode L1 Home armAway", "mode L1 Home armAway")

		if err := r.c.Snapshot(ctx, "C1"); err != nil {
			t.Fatal(err)
		}
		r.expect("snapshot", "snapshot C1 https://snap/C1-new")
		if err := r.c.Snapshot(ctx, "B"); err == nil {
			t.Error("snapshot of a base station")
		}

		u, err := r.c.Stream(ctx, "C1")
		if err != nil || u != "rtsps://1.2.3.4:443/vzmodulelive/C1_1?egressToken=x" {
			t.Errorf("stream %q, %v", u, err)
		}

		rs, err := r.c.Library(ctx, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2000, 1, 3, 0, 0, 0, 0, time.UTC))
		want := Recording{CameraID: "C1", Created: time.UnixMilli(946684800123), Duration: 12 * time.Second,
			ContentType: "video/mp4", Reason: "motionRecord", Object: "Person", URL: "https://v", ThumbnailURL: "https://t"}
		if err != nil || len(rs) != 1 || rs[0] != want {
			t.Errorf("library %+v, %v", rs, err)
		}

		li, err := r.c.LastImages(ctx, "C1")
		if err != nil || li != (LastImages{Image: "https://last/C1", Snapshot: "https://snap/C1"}) {
			t.Errorf("last images %+v, %v", li, err)
		}

		fake.drop()
		r.expect("drop", "down")
		if err := r.c.SetMode(ctx, Standby); !errors.Is(err, ErrNotConnected) {
			t.Errorf("set mode while down: %v", err)
		}
	})
}

func TestRunAlreadyRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startRun(t, newFakeArlo(t), withCode)
		r.expect("connect", connected...)
		if err := r.c.Run(t.Context(), func(Event) {}); !errors.Is(err, ErrAlreadyRunning) {
			t.Errorf("second Run: %v", err)
		}
	})
}
