package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	arlo "github.com/llehouerou/go-arlo"
)

// camera turns a camera on or off and prints events until it reports the
// change.
func camera(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("camera", flag.ExitOnError)
	config := configFlags(fs)
	wait := fs.Duration("wait", time.Minute, "how long to wait for the camera's report")
	_ = fs.Parse(args)
	id, state := fs.Arg(0), fs.Arg(1)
	if id == "" || (state != "on" && state != "off") {
		return errors.New("camera: want <camera id> on|off")
	}
	on := state == "on"
	cfg, err := config()
	if err != nil {
		return err
	}
	done := make(chan struct{}, 1)
	show := func(e arlo.Event) {
		fmt.Println(time.Now().Format(time.TimeOnly), describe(e))
		if s, ok := e.(arlo.DeviceState); ok && s.ID == id && s.On != nil && *s.On == on {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}
	return withRun(ctx, cfg, show, func(ctx context.Context, c *arlo.Client) error {
		sctx, stop := soon(ctx)
		defer stop()
		if err := c.SetCameraOn(sctx, id, on); err != nil {
			return err
		}
		fmt.Println(time.Now().Format(time.TimeOnly), "camera", state, "requested")
		select {
		case <-done:
			return nil
		case <-time.After(*wait):
			return errors.New("camera: no report came")
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}
