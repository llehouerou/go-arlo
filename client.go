// Package arlo is a client for Arlo's cloud API, ported from pyaarlo 0.8.0.23.
package arlo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	defaultAuthHost = "https://ocapi-app.arlo.com"
	defaultAPIHost  = "https://myapi.arlo.com"
	origin          = "https://my.arlo.com"
	// pyaarlo's default ("arlo" in its USER_AGENTS).
	userAgent = "(iPhone15,2 18_1_1) iOS Arlo 5.4.3"
)

// CodeFunc returns the 6-digit code Arlo emails for two-factor
// authentication. since is when the code was requested.
type CodeFunc func(ctx context.Context, since time.Time) (string, error)

// Config configures a Client.
type Config struct {
	Email    string
	Password string
	// SessionPath is the file holding the session between runs: it spares
	// the two-factor step, so it must survive restarts.
	SessionPath string
	// Code supplies the two-factor code. Only needed until the client is a
	// trusted browser.
	Code CodeFunc
	// DumpDir, when set, receives every HTTP response and MQTT message with
	// secrets redacted, to debug without spending auth attempts.
	DumpDir string
	Log     *slog.Logger
}

// Client talks to Arlo on behalf of one account.
type Client struct {
	log *slog.Logger

	// Only Run's goroutine touches api: every other public method hands its
	// work to a connected Run through h.
	api  *api
	dial dialFunc
	h    handoff
}

// New returns a client. It does not touch the network: see Run.
func New(cfg Config) *Client {
	a := newAPI(cfg, defaultAuthHost, defaultAPIHost)
	return newClient(a, mqttDialer(a.log))
}

func newClient(a *api, dial dialFunc) *Client {
	return &Client{log: a.log, api: a, dial: dial, h: handoff{cmds: make(chan command), runDone: make(chan struct{})}}
}

// ErrAlreadyRunning is Run's error while another Run of the same Client is
// going.
var ErrAlreadyRunning = errors.New("arlo: Run already running")

// ErrNotRunning is a command's error when Run has returned and not been
// called again, or returns while the command waits for its connection.
var ErrNotRunning = errors.New("arlo: Run not running")

// handoff carries commands from any goroutine to Run's, so that only Run's
// goroutine touches the session and the stream. One Run at a time holds it;
// a command reaches that Run once connected, waits for one if none has
// returned yet, and fails with ErrNotRunning once it has.
type handoff struct {
	cmds chan command // only a connected Run receives

	mu      sync.Mutex
	running bool
	ran     bool          // a Run has returned
	runDone chan struct{} // closed when the current Run, or the next one, returns
}

// enter claims the handoff for a Run; leave releases it.
func (h *handoff) enter() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return ErrAlreadyRunning
	}
	h.running = true
	return nil
}

func (h *handoff) leave() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running, h.ran = false, true
	close(h.runDone)
	h.runDone = make(chan struct{})
}

// command is work Run does on behalf of another goroutine.
type command struct {
	fn   func(ctx context.Context, st *stream, emit func(Event)) error
	done chan error
}

// run does the command on Run's goroutine and answers its caller.
func (cmd command) run(ctx context.Context, st *stream, emit func(Event)) {
	cmd.done <- cmd.fn(ctx, st, emit)
}

// do hands fn to Run once it is connected and waits for its result, naming
// fn's errors after the command. Called before Run's first call, it waits
// for it. ctx bounds the whole wait: Run may be in a backoff of up to an
// hour.
func (h *handoff) do(ctx context.Context, name string, fn func(context.Context, *stream, func(Event)) error) error {
	h.mu.Lock()
	stopped, runDone := h.ran && !h.running, h.runDone
	h.mu.Unlock()
	if stopped {
		return ErrNotRunning
	}
	cmd := command{fn: fn, done: make(chan error, 1)}
	select {
	case h.cmds <- cmd:
	case <-runDone:
		return ErrNotRunning
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-cmd.done:
		if err != nil {
			return fmt.Errorf("arlo: %s: %w", name, err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
