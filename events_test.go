package arlo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRunOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // Run waits out its backoff
	}))
	defer srv.Close()
	cfg := Config{SessionPath: filepath.Join(t.TempDir(), "session.json")}
	c := newClient(cfg, newAPI(cfg, srv.URL, srv.URL))

	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { first <- c.Run(ctx, func(Event) {}) }()
	for !c.running.Load() {
		time.Sleep(time.Millisecond)
	}
	if err := c.Run(ctx, func(Event) {}); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Run: %v", err)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("first Run: %v", err)
	}
	if err := c.Run(ctx, func(Event) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("Run after the first ended: %v", err)
	}
}
