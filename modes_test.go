package arlo

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetMode(t *testing.T) {
	var put string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hmsweb/automation/v3/activeMode" || r.URL.Query().Get("locationId") != "L1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		if r.Header.Get("x-forwarded-user") != "U1" || r.Header.Get("Authorization") != "tok" {
			t.Errorf("headers %v", r.Header)
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"success":true,"data":{"properties":{"mode":"standby"},"revision":41}}`)
		case http.MethodPut:
			if rev := r.URL.Query().Get("revision"); rev != "41" {
				t.Errorf("revision %q", rev)
			}
			b, _ := io.ReadAll(r.Body)
			put = string(b)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"revision": 42}})
		}
	}))
	defer srv.Close()

	a := newAPI(Config{}, srv.URL, srv.URL)
	a.sess = session{UserID: "U1", Token: "tok"}
	if err := a.setMode(t.Context(), location{ID: "L1"}, ArmHome); err != nil {
		t.Fatal(err)
	}
	if put != `{"mode":"armHome"}` {
		t.Errorf("PUT body %s", put)
	}
}

func TestSetModeNeedsRun(t *testing.T) {
	if err := New(Config{}).SetMode(t.Context(), ArmHome); !errors.Is(err, ErrNotConnected) {
		t.Errorf("err %v", err)
	}
}
