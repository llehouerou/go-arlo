# go-arlo

A Go client for Arlo's cloud API: log in (with email two-factor and trusted
browser pairing), follow the event stream, and read and set the location
mode. The protocol is ported from [pyaarlo](https://github.com/twrecked/pyaarlo)
0.8.0.23.

Scope is deliberately small: device connectivity, battery, motion and the
location's alarm mode. No video, streams, snapshots or library. Tested with a
VMB4000 base station and Arlo Pro 2 cameras on an account using location
(V3) modes, from a granted-access account.

## Library

```go
c := arlo.New(arlo.Config{
	Email:       email,
	Password:    password,
	SessionPath: "/var/lib/app/arlo-session.json",
	Code:        arlo.IMAPCode("imap.example.com:993", imapUser, imapPassword),
})

go func() {
	err := c.Run(ctx, func(e arlo.Event) {
		switch e := e.(type) {
		case arlo.Connection:  // event stream up or down
		case arlo.Devices:     // base stations and cameras, on each connection
		case arlo.DeviceState: // connected and/or battery of one device
		case arlo.Motion:      // motion started or stopped on a camera
		case arlo.ModeChanged: // the location's mode
		}
	})
	// err: ctx ended, or a failure retrying cannot fix
}()

err := c.SetMode(ctx, arlo.ArmHome) // arlo.ErrNotConnected while Run is not connected
```

`Run` blocks. It logs in, connects to Arlo's MQTT broker, pings the base
stations every minute, asks them for their devices' state every ten minutes,
renews the two-hour token before it expires, and reconnects with a backoff.
The handler is called from `Run`'s goroutine, one event at a time.
`SetMode` executes on that same goroutine. A second `Run` of the same client
returns `arlo.ErrAlreadyRunning`.

`arlo.Login(ctx, cfg)` logs in once and saves the session, to get through
the first email two-factor ahead of `Run`. Never alongside a `Run` on the
same session file.

## Things Arlo imposes

- **Auth attempts are rate limited with a long cooldown.** A login never
  retries, reuses the saved token while it is valid, and `Run` waits at
  least a minute (an hour after a refused auth) between attempts.
- **Two-factor is by email until the client is a trusted browser.** The
  first login needs a code (`Config.Code`: `IMAPCode`, or anything returning
  the code); later ones do not.
- **One owner per session file.** Every trusted login rotates the
  `browser_trust` cookie and voids the previous one: a copied session file,
  or one shared by two hosts, falls back to email two-factor.
- Concurrent sessions of one account are tolerated: several processes and
  MQTT connections, and new logins, do not disconnect each other.

`docs/NOTES.md` records what Arlo actually sends, as observed.

## CLI

```
nix develop   # Go toolchain
go build ./cmd/arlo
arlo login -email … -password-file … [-imap-user … -imap-password-file …]
arlo watch  (same flags)   # print events
arlo mode   (same flags) [standby|armHome|armAway]
```

Without `-imap-user` the code is typed on stdin. Responses and MQTT messages
are dumped, secrets redacted, to `-dump` (default `debug/`, empty to
disable): debug from the dumps rather than by spending auth attempts.

## License

MIT
