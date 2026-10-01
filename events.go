package arlo

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"time"

	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
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

// Run follows Arlo's event stream until ctx ends, logging in, renewing the
// token and reconnecting as needed, with a backoff that spares Arlo's auth
// rate limit. handle is called from Run's goroutine, one event at a time.
// Run returns ctx's error, or an error retrying cannot fix.
func (c *Client) Run(ctx context.Context, handle func(Event)) error {
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
	if err := c.Login(ctx); err != nil {
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
	msgs := make(chan *paho.Publish, 64)
	cl, err := c.connectMQTT(ctx, msgs)
	if err != nil {
		return fmt.Errorf("arlo: MQTT: %w", err)
	}
	defer cl.Disconnect(&paho.Disconnect{})
	subs := make([]paho.SubscribeOptions, len(st.topics))
	for i, t := range st.topics {
		subs[i] = paho.SubscribeOptions{Topic: t}
	}
	ack, err := cl.Subscribe(ctx, &paho.Subscribe{Subscriptions: subs})
	if err != nil {
		return fmt.Errorf("arlo: MQTT subscribe: %w", err)
	}
	for i, r := range ack.Reasons {
		if r >= 0x80 && i < len(st.topics) {
			c.log.Warn("arlo: MQTT subscription refused", "topic", st.topics[i], "reason", r)
		}
	}
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
		case <-cl.Done():
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
		case m := <-msgs:
			c.api.dump("mqtt"+m.Topic, map[string]any{"topic": m.Topic}, m.Payload)
			es, err := st.received(m.Payload)
			if errors.Is(err, errLoggedOut) {
				return err
			}
			if err != nil {
				c.log.Warn("arlo: unreadable MQTT message", "topic", m.Topic, "err", err)
				continue
			}
			c.log.Debug("arlo: MQTT message", "topic", m.Topic, "events", len(es))
			emitAll(es)
		}
	}
}

// connectMQTT connects to the broker the session named, as pyaarlo does.
// Received messages go to msgs until ctx ends.
func (c *Client) connectMQTT(ctx context.Context, msgs chan<- *paho.Publish) (*paho.Client, error) {
	u, err := url.Parse(c.api.mqttURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ssl" {
		return nil, fmt.Errorf("unsupported MQTT URL %q", c.api.mqttURL)
	}
	conn, err := (&tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 15 * time.Second},
		Config:    &tls.Config{ServerName: u.Hostname()},
	}).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	cl := paho.NewClient(paho.ClientConfig{
		Conn: packets.NewThreadSafeConn(conn),
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){
			func(pr paho.PublishReceived) (bool, error) {
				select {
				case msgs <- pr.Packet:
				case <-ctx.Done():
				}
				return true, nil
			},
		},
		OnServerDisconnect: func(d *paho.Disconnect) {
			c.log.Warn("arlo: MQTT server disconnect", "reason", d.ReasonCode)
		},
		OnClientError: func(err error) { c.log.Debug("arlo: MQTT client error", "err", err) },
	})
	_, err = cl.Connect(ctx, &paho.Connect{
		// pyaarlo: the last 10 digits must be random.
		ClientID:     fmt.Sprintf("user_%s_%010d", c.api.sess.UserID, rand.IntN(1e10)),
		Username:     c.api.sess.UserID,
		UsernameFlag: true,
		Password:     []byte(c.api.sess.Token),
		PasswordFlag: true,
		KeepAlive:    60,
		CleanStart:   true,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	return cl, nil
}
