package arlo

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/url"
	"time"

	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
)

// streamMessage is one payload received on the Event stream.
type streamMessage struct {
	topic   string
	payload []byte
}

// eventConn is one connection to the Event stream.
type eventConn struct {
	msgs  <-chan streamMessage // received payloads, until dial's ctx ends
	done  <-chan struct{}      // closed when the connection drops
	close func()
}

// dialFunc connects to the Event stream at url as the account and subscribes
// to topics. ctx bounds the whole connection, not just the dial.
type dialFunc func(ctx context.Context, url, userID, token string, topics []string) (eventConn, error)

// mqttDialer connects to Arlo's MQTT broker, as pyaarlo does.
func mqttDialer(log *slog.Logger) dialFunc {
	return func(ctx context.Context, rawURL, userID, token string, topics []string) (eventConn, error) {
		u, err := url.Parse(rawURL)
		if err != nil {
			return eventConn{}, err
		}
		if u.Scheme != "ssl" {
			return eventConn{}, fmt.Errorf("unsupported MQTT URL %q", rawURL)
		}
		conn, err := (&tls.Dialer{
			NetDialer: &net.Dialer{Timeout: 15 * time.Second},
			Config:    &tls.Config{ServerName: u.Hostname()},
		}).DialContext(ctx, "tcp", u.Host)
		if err != nil {
			return eventConn{}, err
		}
		msgs := make(chan streamMessage, 64)
		cl := paho.NewClient(paho.ClientConfig{
			Conn: packets.NewThreadSafeConn(conn),
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					select {
					case msgs <- streamMessage{topic: pr.Packet.Topic, payload: pr.Packet.Payload}:
					case <-ctx.Done():
					}
					return true, nil
				},
			},
			OnServerDisconnect: func(d *paho.Disconnect) {
				log.Warn("arlo: MQTT server disconnect", "reason", d.ReasonCode)
			},
			OnClientError: func(err error) { log.Debug("arlo: MQTT client error", "err", err) },
		})
		if _, err := cl.Connect(ctx, &paho.Connect{
			// pyaarlo: the last 10 digits must be random.
			ClientID:     fmt.Sprintf("user_%s_%010d", userID, rand.IntN(1e10)),
			Username:     userID,
			UsernameFlag: true,
			Password:     []byte(token),
			PasswordFlag: true,
			KeepAlive:    60,
			CleanStart:   true,
		}); err != nil {
			conn.Close()
			return eventConn{}, err
		}
		disconnect := func() { _ = cl.Disconnect(&paho.Disconnect{}) }
		subs := make([]paho.SubscribeOptions, len(topics))
		for i, t := range topics {
			subs[i] = paho.SubscribeOptions{Topic: t}
		}
		ack, err := cl.Subscribe(ctx, &paho.Subscribe{Subscriptions: subs})
		if err != nil {
			disconnect()
			return eventConn{}, fmt.Errorf("subscribe: %w", err)
		}
		for i, r := range ack.Reasons {
			if r >= 0x80 && i < len(topics) {
				log.Warn("arlo: MQTT subscription refused", "topic", topics[i], "reason", r)
			}
		}
		return eventConn{msgs: msgs, done: cl.Done(), close: disconnect}, nil
	}
}
