package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	arlo "github.com/llehouerou/go-arlo"
)

// snapshot asks a camera for a snapshot and prints events until it comes.
func snapshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	config := configFlags(fs)
	wait := fs.Duration("wait", time.Minute, "how long to wait for the snapshot")
	_ = fs.Parse(args)
	camera := fs.Arg(0)
	if camera == "" {
		return errors.New("snapshot: missing camera id")
	}
	cfg, err := config()
	if err != nil {
		return err
	}
	ready := make(chan struct{}, 1)
	show := func(e arlo.Event) {
		fmt.Println(time.Now().Format(time.TimeOnly), describe(e))
		if s, ok := e.(arlo.SnapshotReady); ok && s.ID == camera {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	}
	return connected(ctx, cfg, show, func(ctx context.Context, c *arlo.Client) error {
		if err := c.Snapshot(ctx, camera); err != nil {
			return err
		}
		fmt.Println(time.Now().Format(time.TimeOnly), "snapshot requested")
		select {
		case <-ready:
			return nil
		case <-time.After(*wait):
			return errors.New("snapshot: none came")
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}
