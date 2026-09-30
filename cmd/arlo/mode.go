package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	arlo "github.com/llehouerou/go-arlo"
)

// mode prints the location's mode and, given one, sets it. It runs the event
// stream for a few seconds to show Arlo's confirmation.
func mode(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mode", flag.ExitOnError)
	client := clientFlags(fs)
	_ = fs.Parse(args)
	target := arlo.Mode(fs.Arg(0))
	switch target {
	case "", arlo.Standby, arlo.ArmHome, arlo.ArmAway:
	default:
		return fmt.Errorf("mode: %q is not standby, armHome or armAway", target)
	}
	c, err := client()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	moded := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(e arlo.Event) {
			switch e.(type) {
			case arlo.ModeChanged, arlo.Connection:
				fmt.Println(time.Now().Format(time.TimeOnly), describe(e))
			}
			if _, ok := e.(arlo.ModeChanged); ok {
				select {
				case moded <- struct{}{}:
				default:
				}
			}
		})
	}()
	select { // the first ModeChanged comes once connected
	case <-moded:
	case err := <-done:
		return err
	case <-time.After(time.Minute):
		return errors.New("mode: no mode read within a minute; see the logs")
	}
	if target != "" {
		if err := c.SetMode(ctx, target); err != nil {
			return err
		}
	}
	select {
	case <-time.After(10 * time.Second):
	case err := <-done:
		return err
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
