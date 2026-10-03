package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	arlo "github.com/llehouerou/go-arlo"
)

// stream starts a camera's live stream and prints its URL, then plays it
// with -play, or prints events until interrupted or -wait ends.
func stream(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("stream", flag.ExitOnError)
	config := configFlags(fs)
	wait := fs.Duration("wait", 2*time.Minute, "how long to print events after the URL, without -play")
	play := fs.String("play", "", `player command the URL is appended to, e.g. "mpv"; it must skip TLS verification`)
	_ = fs.Parse(args)
	camera := fs.Arg(0)
	if camera == "" {
		return errors.New("stream: missing camera id")
	}
	player := strings.Fields(*play)
	cfg, err := config()
	if err != nil {
		return err
	}
	show := func(e arlo.Event) { fmt.Println(time.Now().Format(time.TimeOnly), describe(e)) }
	return connected(ctx, cfg, show, func(ctx context.Context, c *arlo.Client) error {
		u, err := c.Stream(ctx, camera)
		if err != nil {
			return err
		}
		fmt.Println(time.Now().Format(time.TimeOnly), "stream", u)
		if len(player) > 0 {
			cmd := exec.CommandContext(ctx, player[0], append(player[1:], u)...)
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			return cmd.Run()
		}
		select {
		case <-time.After(*wait):
		case <-ctx.Done():
		}
		return nil
	})
}
