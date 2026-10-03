package arlo

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLogin(t *testing.T) {
	fake := newFakeArlo(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "session.json")

	codes := 0
	login := func() error {
		cfg := Config{
			Email: "me@example.com", Password: "secret", SessionPath: path,
			Code: func(context.Context, time.Time) (string, error) { codes++; return "123456", nil },
		}
		c := newClient(cfg, newAPI(cfg, srv.URL, srv.URL), nil)
		if err := c.login(t.Context()); err != nil {
			return err
		}
		if c.api.mqttURL != "ssl://mqtt.example:8883" {
			t.Errorf("mqttUrl %q", c.api.mqttURL)
		}
		return nil
	}
	expect := func(step string, want ...string) {
		t.Helper()
		if got := fake.takeCalls(); !slices.Equal(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", step, got, want)
		}
	}

	// First run: email 2FA, then pairing.
	if err := login(); err != nil {
		t.Fatal(err)
	}
	expect("first login",
		"/api/auth", "/api/getFactorId", "/api/getFactors", "/api/startAuth",
		"/api/finishAuth", "/api/validateAccessToken", "/api/startPairingFactor",
		"/hmsweb/users/session/v3")
	if codes != 1 {
		t.Errorf("code asked %d times", codes)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session file: %v %v", info, err)
	}

	// Second run: the saved token is still valid, no auth at all.
	if err := login(); err != nil {
		t.Fatal(err)
	}
	expect("token reuse", "/api/validateAccessToken", "/hmsweb/users/session/v3")

	// Token expired, twice: one auth each, trusted browser, no code. The
	// second only passes if the rotated trust cookie was saved.
	for range 2 {
		fake.issued = "expired"
		if err := login(); err != nil {
			t.Fatal(err)
		}
		expect("trusted browser",
			"/api/validateAccessToken", "/api/auth", "/api/getFactorId", "/api/startAuth",
			"/api/validateAccessToken", "/hmsweb/users/session/v3")
	}
	if codes != 1 {
		t.Errorf("code asked %d times", codes)
	}

	// Refused auth: exactly one attempt, then an error.
	fake.issued, fake.refuse = "expired", true
	if err := login(); err == nil {
		t.Fatal("login succeeded against a refusing server")
	}
	expect("refused", "/api/validateAccessToken", "/api/auth")
}

func TestCodeFromMail(t *testing.T) {
	raw := "From: Arlo <do_not_reply@arlo.com>\r\n" +
		"Subject: Your one-time authentication code\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Your code expires in 15 minutes. Account 555123.\r\n\r\n   482913=20\r\n\r\n" +
		"--b\r\nContent-Type: text/html\r\n\r\n<p>482913</p>\r\n--b--\r\n"
	if got := codeFromMail([]byte(raw)); got != "482913" {
		t.Errorf("code %q", got)
	}
	if got := codeFromMail([]byte("Subject: hi\r\n\r\nno code here 123\r\n")); got != "" {
		t.Errorf("code %q", got)
	}
}
