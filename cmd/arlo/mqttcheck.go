package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
)

// mqttCheck connects to Arlo's broker with MQTT v5 using a saved session,
// subscribes to the user session topics and prints what arrives. Spike for
// choosing the MQTT library; the event stream proper will replace it.
func mqttCheck(args []string) error {
	fs := flag.NewFlagSet("mqtt-check", flag.ExitOnError)
	sessionPath := fs.String("session", "arlo.session.json", "session file written by login")
	rawURL := fs.String("url", "", "mqttUrl logged by login")
	wait := fs.Duration("wait", 20*time.Second, "how long to listen")
	_ = fs.Parse(args)

	b, err := os.ReadFile(*sessionPath)
	if err != nil {
		return err
	}
	var s struct{ UserID, Token string }
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	u, err := url.Parse(*rawURL)
	if err != nil || u.Host == "" {
		return errors.New("mqtt-check: -url must be the mqttUrl logged by login")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *wait+30*time.Second)
	defer cancel()
	conn, err := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second},
		Config: &tls.Config{ServerName: u.Hostname()}}).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return err
	}
	cl := paho.NewClient(paho.ClientConfig{
		Conn: packets.NewThreadSafeConn(conn),
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){
			func(pr paho.PublishReceived) (bool, error) {
				fmt.Printf("message %s: %.300s\n", pr.Packet.Topic, pr.Packet.Payload)
				return true, nil
			},
		},
		OnServerDisconnect: func(d *paho.Disconnect) { fmt.Printf("server disconnect: reason %d\n", d.ReasonCode) },
		OnClientError:      func(err error) { fmt.Println("client error:", err) },
	})
	ack, err := cl.Connect(ctx, &paho.Connect{
		ClientID:     fmt.Sprintf("user_%s_%010d", s.UserID, rand.IntN(1e10)),
		Username:     s.UserID,
		UsernameFlag: true,
		Password:     []byte(s.Token),
		PasswordFlag: true,
		KeepAlive:    60,
		CleanStart:   true,
	})
	if ack != nil {
		fmt.Printf("connack: reason %d session-present %v\n", ack.ReasonCode, ack.SessionPresent)
	}
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	suback, err := cl.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{
		{Topic: "u/" + s.UserID + "/in/userSession/connect"},
		{Topic: "u/" + s.UserID + "/in/userSession/disconnect"},
	}})
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	fmt.Printf("suback: reasons %v; listening %s\n", suback.Reasons, *wait)
	time.Sleep(*wait)
	return cl.Disconnect(&paho.Disconnect{})
}
