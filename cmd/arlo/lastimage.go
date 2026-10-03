package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	arlo "github.com/llehouerou/go-arlo"
)

// lastImage prints a camera's latest pictures with their dates, without
// waking it, and saves the newest with -o.
func lastImage(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("lastimage", flag.ExitOnError)
	config := configFlags(fs)
	out := fs.String("o", "", "file to save the newest picture to")
	_ = fs.Parse(args)
	camera := fs.Arg(0)
	if camera == "" {
		return errors.New("lastimage: missing camera id")
	}
	cfg, err := config()
	if err != nil {
		return err
	}
	return connected(ctx, cfg, func(arlo.Event) {}, func(ctx context.Context, c *arlo.Client) error {
		li, err := c.LastImages(ctx, camera)
		if err != nil {
			return err
		}
		var newest []byte
		var newestAt time.Time
		for _, p := range []struct{ kind, url string }{{"image", li.Image}, {"snapshot", li.Snapshot}} {
			if p.url == "" {
				fmt.Println(p.kind, "none")
				continue
			}
			body, at, err := fetch(ctx, p.url)
			if err != nil {
				return fmt.Errorf("%s: %w", p.kind, err)
			}
			fmt.Printf("%-8s %s %6d bytes %s\n", p.kind, at.Local().Format(time.DateTime), len(body), p.url)
			if at.After(newestAt) {
				newest, newestAt = body, at
			}
		}
		if *out == "" || newest == nil {
			return nil
		}
		return os.WriteFile(*out, newest, 0o644)
	})
}

// fetch GETs a presigned URL and returns its body and Last-Modified date.
func fetch(ctx context.Context, url string) ([]byte, time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, time.Time{}, err
	}
	at, _ := http.ParseTime(resp.Header.Get("Last-Modified")) // zero when absent
	return body, at, nil
}
