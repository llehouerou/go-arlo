// Command arlo exercises the go-arlo library by hand.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "probe":
		err = probe()
	case "login":
		err = login(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "arlo:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: arlo <command>

commands:
  probe   check that Cloudflare lets us reach Arlo (no credentials sent)
  login   open a session, with 2FA if needed, and save it (-h for flags)`)
	os.Exit(2)
}
