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
		fmt.Printf("%s %#v\n", time.Now().Format(time.TimeOnly), e)
	})
}
