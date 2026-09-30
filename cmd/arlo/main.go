// Command arlo exercises the go-arlo library by hand.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "probe":
		err = probe()
	case "login":
		err = login(ctx, os.Args[2:])
	case "watch":
		err = watch(ctx, os.Args[2:])
	case "mode":
		err = mode(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "arlo:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: arlo <command> [flags]

commands:
  probe   check that Cloudflare lets us reach Arlo (no credentials sent)
  login   open a session, with 2FA if needed, and save it
  watch   follow Arlo's event stream and print events
  mode    print the location's mode; mode [flags] standby|armHome|armAway sets it

login, watch and mode take the same flags; see arlo login -h.`)
	os.Exit(2)
}
