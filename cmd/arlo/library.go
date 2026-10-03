package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	arlo "github.com/llehouerou/go-arlo"
)

// library prints the recordings of the last days.
func library(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("library", flag.ExitOnError)
	config := configFlags(fs)
	days := fs.Int("days", 7, "days to list, today included")
	urls := fs.Bool("urls", false, "print the presigned media URLs")
	_ = fs.Parse(args)
	cfg, err := config()
	if err != nil {
		return err
	}
	names := map[string]string{}
	return connected(ctx, cfg, func(e arlo.Event) {
		if ds, ok := e.(arlo.Devices); ok {
			for _, d := range ds {
				names[d.ID] = d.Name
			}
		}
	}, func(ctx context.Context, c *arlo.Client) error {
		now := time.Now()
		rs, err := c.Library(ctx, now.AddDate(0, 0, 1-*days), now)
		if err != nil {
			return err
		}
		for _, r := range rs {
			fmt.Printf("%s %-10s %-10s %4ds %-12s %s\n", r.Created.Format(time.DateTime), names[r.CameraID],
				r.ContentType, int(r.Duration.Seconds()), r.Reason, r.Object)
			if *urls {
				fmt.Printf("  %s\n  %s\n", r.URL, r.ThumbnailURL)
			}
		}
		fmt.Println(len(rs), "recordings")
		return nil
	})
}

// connected runs a client with handle until it is connected, then runs fn
// and stops the client.
func connected(ctx context.Context, cfg arlo.Config, handle func(arlo.Event), fn func(context.Context, *arlo.Client) error) error {
	c := arlo.New(cfg)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	up := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(e arlo.Event) {
			handle(e)
			if cn, ok := e.(arlo.Connection); ok && cn.Up {
				select {
				case up <- struct{}{}:
				default:
				}
			}
		})
	}()
	select {
	case <-up:
	case err := <-done:
		return err
	case <-time.After(time.Minute):
		return errors.New("not connected within a minute; see the logs")
	}
	if err := fn(ctx, c); err != nil {
		return err
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
