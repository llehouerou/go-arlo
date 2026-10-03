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
	case "login":
		err = login(ctx, os.Args[2:])
	case "watch":
		err = watch(ctx, os.Args[2:])
	case "mode":
		err = mode(ctx, os.Args[2:])
	case "library":
		err = library(ctx, os.Args[2:])
	case "snapshot":
		err = snapshot(ctx, os.Args[2:])
	case "stream":
		err = stream(ctx, os.Args[2:])
	case "lastimage":
		err = lastImage(ctx, os.Args[2:])
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
  login   open a session, with 2FA if needed, and save it
  watch   follow Arlo's event stream and print events
  mode    print the location's mode; mode [flags] standby|armHome|armAway sets it
  library list the recordings of the last days (-days, -urls)
  snapshot [flags] <camera id>  ask a camera for a snapshot, print events
  stream [flags] <camera id>    start a camera's live stream, print its URL and events
  lastimage [flags] <camera id> print a camera's latest pictures without waking it; -o saves the newest

Commands take the same flags; see arlo login -h.`)
	os.Exit(2)
}
