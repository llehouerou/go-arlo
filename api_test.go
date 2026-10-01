package arlo

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDumpRedacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"data":{"userId":"secret-user","deviceName":"Gate",`+
			`"owner":{"Email":"secret@mail"},"items":[{"token":"secret-tok","streamURL":"rtsp://secret"}]}}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := newAPI(Config{DumpDir: dir}, srv.URL, srv.URL)
	if _, err := a.apiCall(t.Context(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("dumps %v", files)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret") {
		t.Errorf("secret in dump:\n%s", b)
	}
	if !strings.Contains(string(b), `"Gate"`) || strings.Count(string(b), "REDACTED") != 4 {
		t.Errorf("dump:\n%s", b)
	}
}

func TestUnwrap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		data   string // "" when an apiError is expected
	}{
		{"auth host", 200, `{"meta":{"code":200},"data":{"a":1}}`, `{"a":1}`},
		{"auth refusal", 200, `{"meta":{"code":400,"message":"no"}}`, ""},
		{"auth host, HTTP error", 401, `{"meta":{"code":200},"data":{}}`, ""},
		{"api host", 200, `{"success":true,"data":[1]}`, `[1]`},
		{"api host, no data", 200, `{"success":true}`, `{}`},
		{"api refusal", 200, `{"success":false,"data":{"reason":"no"}}`, ""},
		{"api host, HTTP error", 500, `{"success":true}`, ""},
		{"not JSON", 403, `<html>Cloudflare</html>`, ""},
	} {
		got, err := unwrap("/p", tc.status, []byte(tc.body))
		var refused *apiError
		switch {
		case tc.data == "" && !errors.As(err, &refused):
			t.Errorf("%s: err %v, want an apiError", tc.name, err)
		case tc.data != "" && (err != nil || string(got) != tc.data):
			t.Errorf("%s: got %s, %v", tc.name, got, err)
		}
	}
}
