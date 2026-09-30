package arlo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeArlo plays both Arlo hosts. The browser is trusted once the pairing
// cookie it sets comes back; only tokens it issued validate.
type fakeArlo struct {
	t     *testing.T
	mu    sync.Mutex
	calls []string
	// issued is the last token handed out; "" validates nothing.
	issued   string
	refuse   bool // /api/auth answers 401
	deviceID string
}

func (f *fakeArlo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	f.calls = append(f.calls, r.URL.Path)
	if r.URL.Path != "/hmsweb/users/session/v3" {
		if id := r.Header.Get("X-User-Device-Id"); f.deviceID == "" {
			f.deviceID = id
		} else if id != f.deviceID {
			f.t.Errorf("%s: device id %q, want %q", r.URL.Path, id, f.deviceID)
		}
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	authorized := r.Header.Get("Authorization") == base64.StdEncoding.EncodeToString([]byte(f.issued)) && f.issued != ""
	trusted := false
	if ck, err := r.Cookie("trust"); err == nil && ck.Value == "yes" {
		trusted = true
	}
	meta := func(data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"code": 200}, "data": data})
	}
	refuse := func(code int) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"code": code, "error": 9204, "message": "no"}})
	}
	token := func() string {
		f.issued = "tok-" + time.Now().Format("150405.000000000")
		return f.issued
	}
	expires := time.Now().Add(2 * time.Hour).Unix()

	switch r.URL.Path {
	case "/api/auth":
		if f.refuse {
			refuse(http.StatusUnauthorized)
			return
		}
		if pw, _ := base64.StdEncoding.DecodeString(body["password"].(string)); string(pw) != "secret" {
			f.t.Errorf("password %q", pw)
		}
		meta(map[string]any{"token": token(), "userId": "U1", "expiresIn": expires, "authCompleted": false})
	case "/api/getFactorId":
		if !authorized || !trusted {
			// What Arlo really answers for an untrusted browser.
			_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"code": 400, "error": 9261, "message": "Invalid factor data"}})
			return
		}
		meta(map[string]any{"factorId": "F-browser"})
	case "/api/getFactors":
		meta(map[string]any{"items": []any{
			map[string]any{"factorId": "F-sms", "factorType": "SMS"},
			map[string]any{"factorId": "F-mail", "factorType": "EMAIL"},
		}})
	case "/api/startAuth":
		switch body["factorId"] {
		case "F-browser":
			meta(map[string]any{"accessToken": map[string]any{"token": token(), "userId": "U1", "expiresIn": expires}})
		case "F-mail":
			meta(map[string]any{"factorAuthCode": "FAC"})
		default:
			f.t.Errorf("startAuth factor %v", body["factorId"])
		}
	case "/api/finishAuth":
		if body["otp"] != "123456" || body["factorAuthCode"] != "FAC" || body["isBrowserTrusted"] != true {
			f.t.Errorf("finishAuth body %v", body)
		}
		meta(map[string]any{"accessToken": map[string]any{"token": token(), "userId": "U1", "expiresIn": expires, "browserAuthCode": "BAC"}})
	case "/api/validateAccessToken":
		if !authorized {
			refuse(http.StatusUnauthorized)
			return
		}
		meta(map[string]any{})
	case "/api/startPairingFactor":
		if body["factorAuthCode"] != "BAC" {
			f.t.Errorf("pairing body %v", body)
		}
		http.SetCookie(w, &http.Cookie{Name: "trust", Value: "yes", Path: "/"})
		meta(map[string]any{})
	case "/hmsweb/users/session/v3":
		if r.Header.Get("Authorization") != f.issued || r.URL.Query().Get("eventId") == "" {
			f.t.Errorf("session/v3 auth %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
			"mqttUrl": "ssl://mqtt.example:8883", "supportsMultiLocation": true,
		}})
	default:
		f.t.Errorf("unexpected %s", r.URL.Path)
	}
}

func (f *fakeArlo) takeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func TestLogin(t *testing.T) {
	fake := &fakeArlo{t: t}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "session.json")

	codes := 0
	login := func() error {
		c := New(Config{
			Email: "me@example.com", Password: "secret", SessionPath: path,
			Code: func(context.Context, time.Time) (string, error) { codes++; return "123456", nil },
		})
		c.authHost, c.apiHost = srv.URL, srv.URL
		if err := c.Login(t.Context()); err != nil {
			return err
		}
		if c.mqttURL != "ssl://mqtt.example:8883" || !c.multiLocation {
			t.Errorf("session: %q %v", c.mqttURL, c.multiLocation)
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

	// Token expired: one auth, trusted browser, no code.
	fake.issued = "expired"
	if err := login(); err != nil {
		t.Fatal(err)
	}
	expect("trusted browser",
		"/api/validateAccessToken", "/api/auth", "/api/getFactorId", "/api/startAuth",
		"/api/validateAccessToken", "/hmsweb/users/session/v3")
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
