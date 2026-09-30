package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	arlo "github.com/llehouerou/go-arlo"
)

func watch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	client := clientFlags(fs)
	_ = fs.Parse(args)
	c, err := client()
	if err != nil {
		return err
	}
	return c.Run(ctx, func(e arlo.Event) {
		fmt.Println(time.Now().Format(time.TimeOnly), describe(e))
	})
}

func describe(e arlo.Event) string {
	switch e := e.(type) {
	case arlo.Connection:
		return fmt.Sprintf("connection up=%v", e.Up)
	case arlo.Devices:
		s := "devices:"
		for _, d := range e {
			s += fmt.Sprintf("\n  %s %s %q (%s) base=%q", d.Type, d.ID, d.Name, d.Model, d.BaseID)
		}
		return s
	case arlo.DeviceState:
		s := "state " + e.ID
		if e.Connected != nil {
			s += fmt.Sprintf(" connected=%v", *e.Connected)
		}
		if e.Battery != nil {
			s += fmt.Sprintf(" battery=%d%%", *e.Battery)
		}
		return s
	case arlo.Motion:
		return fmt.Sprintf("motion %s active=%v", e.ID, e.Active)
	}
	return fmt.Sprintf("%#v", e)
}
