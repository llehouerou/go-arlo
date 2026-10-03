// Package arlo is a client for Arlo's cloud API, ported from pyaarlo 0.8.0.23.
package arlo

import (
	"context"
	"log/slog"
	"sync/atomic"
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
	cfg Config
	log *slog.Logger

	// Only Run's goroutine touches api: Run refuses to run twice at once,
	// and every other public method hands its work to Run through do.
	api  *api
	dial dialFunc

	cmds      chan command
	running   atomic.Bool
	connected atomic.Bool
}

// New returns a client. It does not touch the network: see Run.
func New(cfg Config) *Client {
	a := newAPI(cfg, defaultAuthHost, defaultAPIHost)
	return newClient(cfg, a, mqttDialer(a.log))
}

func newClient(cfg Config, a *api, dial dialFunc) *Client {
	return &Client{cfg: cfg, log: a.log, api: a, dial: dial, cmds: make(chan command)}
}
