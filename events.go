package arlo

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Event is what Run reports. DeviceState, Motion and ModeChanged report
// states, which may repeat: a base station sends its packets once per client
// subscribed to it, such as an open Arlo app of any account sharing it.
type Event interface{ isEvent() }

// Connection reports whether the client follows Arlo's event stream. The
// token renewal every two hours does not show: only real outages do.
type Connection struct{ Up bool }

func (Connection) isEvent() {}

var (
	errRenew     = errors.New("token renewal due")
	errLoggedOut = errors.New("logged out by Arlo: did another session take the account?")
)

// ErrAlreadyRunning is Run's error while another Run of the same Client is
// going.
var ErrAlreadyRunning = errors.New("arlo: Run already running")

// Run follows Arlo's event stream until ctx ends, logging in, renewing the
// token and reconnecting as needed, with a backoff that spares Arlo's auth
// rate limit. handle is called from Run's goroutine, one event at a time.
// Run returns ctx's error, or an error retrying cannot fix.
func (c *Client) Run(ctx context.Context, handle func(Event)) error {
	if !c.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	defer c.running.Store(false)
	up := false
	emit := func(e Event) {
		if cn, ok := e.(Connection); ok {
			if cn.Up == up {
				return
			}
			up = cn.Up
		}
		handle(e)
	}
	backoff := time.Minute
	for {
		start := time.Now()
		err := c.follow(ctx, emit)
		switch {
		case ctx.Err() != nil:
			emit(Connection{Up: false})
			return ctx.Err()
		case errors.Is(err, errRenew):
			c.log.Info("arlo: renewing token")
			continue
		case errors.Is(err, errNeedsCode):
			emit(Connection{Up: false})
			return err
		}
		emit(Connection{Up: false})
		if time.Since(start) > 10*time.Minute {
			backoff = time.Minute
		}
		wait := backoff
		if errors.Is(err, errAuthRefused) {
			wait = max(wait, time.Hour)
		}
		c.log.Warn("arlo: event stream down", "err", err, "retry_in", wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff = min(backoff*2, 30*time.Minute)
	}
}

// follow runs one session: login, MQTT, base pings, until the connection
// drops or the token is due for renewal.
func (c *Client) follow(ctx context.Context, emit func(Event)) error {
	if err := c.login(ctx); err != nil {
		return err
	}
	renewIn := time.Until(c.api.expires()) - renewBefore
	if renewIn <= 0 {
		return fmt.Errorf("arlo: token expires at %v, too soon to use", c.api.expires())
	}
	devs, err := c.api.devices(ctx)
	if err != nil {
		return fmt.Errorf("arlo: %w", err)
	}
	st := newStream(c.api.sess.UserID, devs)
	emitAll := func(es []Event) {
		for _, e := range es {
			emit(e)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := c.dial(ctx, c.api.mqttURL, c.api.sess.UserID, c.api.sess.Token, st.topics)
	if err != nil {
		return fmt.Errorf("arlo: MQTT: %w", err)
	}
	defer conn.close()
	c.log.Info("arlo: event stream up", "topics", len(st.topics), "bases", len(st.bases), "renew_in", renewIn.Round(time.Second))
	emit(st.devices)
	emit(Connection{Up: true})
	c.connected.Store(true)
	defer c.connected.Store(false)

	// A base is connected while it answers pings; pyaarlo pings every
	// minute.
	pingAll := func() error {
		for _, b := range st.bases {
			err := c.api.ping(ctx, b)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			es, fatal := st.pinged(b, err)
			if fatal != nil {
				return fatal
			}
			if err != nil {
				c.log.Warn("arlo: ping failed", "base", b.Name, "err", err)
			}
			emitAll(es)
		}
		return nil
	}
	// Bases answer on the event stream with their devices' state; pyaarlo
	// asks every ten minutes. The mode is read at the same pace, in case a
	// change slipped past the event stream.
	refreshAll := func() {
		for _, b := range st.bases {
			err := c.api.notify(ctx, b, map[string]any{"action": "get", "resource": "devices", "publishResponse": false})
			if err != nil && ctx.Err() == nil {
				c.log.Warn("arlo: state refresh failed", "base", b.Name, "err", err)
			}
		}
		if err := c.readMode(ctx, st, emit); err != nil && ctx.Err() == nil {
			c.log.Warn("arlo: mode refresh failed", "err", err)
		}
	}

	renew := time.NewTimer(renewIn)
	defer renew.Stop()
	ping := time.NewTicker(time.Minute)
	defer ping.Stop()
	refresh := time.NewTicker(10 * time.Minute)
	defer refresh.Stop()
	if err := pingAll(); err != nil {
		return err
	}
	refreshAll()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-conn.done:
			return errors.New("arlo: MQTT connection lost")
		case <-renew.C:
			return errRenew
		case <-ping.C:
			if err := pingAll(); err != nil {
				return err
			}
		case <-refresh.C:
			refreshAll()
		case cmd := <-c.cmds:
			cmd.done <- cmd.fn(ctx, st, emit)
		case m := <-conn.msgs:
			c.api.dump("mqtt"+m.topic, map[string]any{"topic": m.topic}, m.payload)
			es, err := st.received(m.payload)
			if errors.Is(err, errLoggedOut) {
				return err
			}
			if err != nil {
				c.log.Warn("arlo: unreadable MQTT message", "topic", m.topic, "err", err)
				continue
			}
			c.log.Debug("arlo: MQTT message", "topic", m.topic, "events", len(es))
			emitAll(es)
		}
	}
}
