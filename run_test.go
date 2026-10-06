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
	cancel context.CancelFunc
}

// startRun starts Run on a fresh Session against fake, over an in-memory
// network. code nil means the account cannot do two-factor.
func startRun(t *testing.T, fake *fakeArlo, code CodeFunc) *testRun {
	r := newTestRun(t, fake, code)
	r.start()
	return r
}

// newTestRun is startRun without starting Run.
func newTestRun(t *testing.T, fake *fakeArlo, code CodeFunc) *testRun {
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
	r := &testRun{t: t, c: newClient(a, fake.dial), done: make(chan struct{}), cancel: func() {}}
	close(r.done) // until start
	t.Cleanup(func() {
		r.stop()
		_ = srv.Close()
	})
	return r
}

func (r *testRun) start() {
	var ctx context.Context
	ctx, r.cancel = context.WithCancel(r.t.Context())
	r.done = make(chan struct{})
	go func() {
		r.err = r.c.Run(ctx, func(e Event) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, describe(e))
		})
		close(r.done)
	}()
}

// stop ends Run and waits for it.
func (r *testRun) stop() {
	r.cancel()
	<-r.done
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
		want := []string{"d/XB/out/cameras/#", "d/XB/out/devices/#", "u/U1/in/library/add", "u/U1/in/library/update", "u/U1/in/userSession/connect", "u/U1/in/userSession/disconnect"}
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

		// Latest pictures come from the device list read on connection,
		// without a request.
		fake.takeCalls()
		li, err := r.c.LastImages(ctx, "C1")
		if err != nil || li != (LastImages{Image: "https://last/C1", Snapshot: "https://snap/C1"}) {
			t.Errorf("last images %+v, %v", li, err)
		}
		if calls := fake.takeCalls(); calls != nil {
			t.Errorf("last images calls %q", calls)
		}
		if _, err := r.c.LastImages(ctx, "B"); err == nil || err.Error() != "arlo: last images: no camera B" {
			t.Errorf("last images of a base station: %v", err)
		}

		if err := r.c.Snapshot(ctx, "C1"); err != nil {
			t.Fatal(err)
		}
		r.expect("snapshot", "snapshot C1 https://snap/C1-new")
		// SnapshotReady updates the latest snapshot.
		if li, err := r.c.LastImages(ctx, "C1"); err != nil || li.Snapshot != "https://snap/C1-new" {
			t.Errorf("last images after a snapshot %+v, %v", li, err)
		}
		if err := r.c.Snapshot(ctx, "B"); err == nil || err.Error() != "arlo: snapshot: no camera B" {
			t.Errorf("snapshot of a base station: %v", err)
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

		// Commands wait through a reconnection.
		fake.drop()
		r.expect("drop", "down")
		cmd := make(chan error, 1)
		go func() { cmd <- r.c.SetMode(ctx, Standby) }()
		synctest.Wait()
		select {
		case err := <-cmd:
			t.Fatalf("set mode while down: %v", err)
		default:
		}
		time.Sleep(time.Minute)
		if err := <-cmd; err != nil {
			t.Fatalf("set mode after reconnecting: %v", err)
		}
		if es := r.take(); !slices.Contains(es, "up") || !slices.Contains(es, "mode L1 Home standby") {
			t.Errorf("reconnect and set mode: %q", es)
		}

		// A waiting command ends with Run.
		fake.drop()
		r.expect("drop again", "down")
		go func() { cmd <- r.c.Snapshot(ctx, "C1") }()
		synctest.Wait()
		r.stop()
		if err := <-cmd; err != ErrNotRunning {
			t.Errorf("snapshot after Run stopped: %v", err)
		}
	})
}

// A command called before Run waits for its connection, as a host calls one
// right after go c.Run(...); once Run has returned, commands fail.
func TestCommandsBeforeRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newTestRun(t, newFakeArlo(t), withCode)
		ctx := t.Context()
		cmd := make(chan error, 1)
		go func() {
			_, err := r.c.LastImages(ctx, "C1")
			cmd <- err
		}()
		synctest.Wait()
		r.start()
		if err := <-cmd; err != nil {
			t.Errorf("last images before Run: %v", err)
		}

		r.stop()
		if _, err := r.c.LastImages(ctx, "C1"); err != ErrNotRunning {
			t.Errorf("last images after Run: %v", err)
		}
	})
}

// A Run that fails before connecting ends the commands waiting for it.
func TestCommandsWaitingRunFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newTestRun(t, newFakeArlo(t), nil) // needs a code it cannot get
		cmd := make(chan error, 1)
		go func() { cmd <- r.c.SetMode(t.Context(), ArmHome) }()
		synctest.Wait()
		r.start()
		if err := <-cmd; err != ErrNotRunning {
			t.Errorf("set mode: %v", err)
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
