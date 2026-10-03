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
	return withRun(ctx, cfg, func(e arlo.Event) {
		if ds, ok := e.(arlo.Devices); ok {
			for _, d := range ds {
				names[d.ID] = d.Name
			}
		}
	}, func(ctx context.Context, c *arlo.Client) error {
		now := time.Now()
		lctx, stop := soon(ctx)
		defer stop()
		rs, err := c.Library(lctx, now.AddDate(0, 0, 1-*days), now)
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

// withRun runs a client with handle, then fn, then stops the client. fn's
// library calls wait for Run's connection: bound each with soon.
func withRun(ctx context.Context, cfg arlo.Config, handle func(arlo.Event), fn func(context.Context, *arlo.Client) error) error {
	c := arlo.New(cfg)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, handle) }()
	err := fn(ctx, c)
	switch {
	case errors.Is(err, arlo.ErrNotRunning):
		return <-done
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("not connected within a minute; see the logs")
	case err != nil:
		return err
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// soon bounds a library call, which waits for Run's connection.
func soon(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Minute)
}
