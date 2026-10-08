# go-arlo

A Go client for Arlo's cloud API: log in (with email two-factor and trusted
browser pairing), follow the event stream, and read and set the location
mode. The protocol was observed on Arlo's servers and cross-checked against
[pyaarlo](https://github.com/twrecked/pyaarlo) 0.8.0.23.

Scope is deliberately small: device connectivity, battery, motion, the
location's alarm mode, the recordings library, the cameras' latest pictures,
snapshots on demand, live stream URLs and turning cameras on or off. Tested
with a VMB4000 base station and Arlo Pro 2 cameras on an account using
location (V3) modes, from a granted-access account.

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
		case arlo.DeviceState: // connected, battery and/or on of one device
		case arlo.Motion:      // motion started or stopped on a camera
		case arlo.ModeChanged: // the location's mode
		case arlo.SnapshotReady: // a camera's new snapshot, whoever asked
		case arlo.RecordingAdded: // a new recording in the library, with its URLs
		}
	})
	// err: ctx ended, or a failure retrying cannot fix
}()

err := c.SetMode(ctx, arlo.ArmHome) // waits for Run's connection; arlo.ErrNotRunning without Run
rs, err := c.Library(ctx, from, to) // recordings of these days, with presigned URLs
err := c.Snapshot(ctx, cameraID)    // SnapshotReady follows within seconds; wakes the camera
u, err := c.Stream(ctx, cameraID)   // rtsps:// URL; read it within ~30 s, skipping TLS verification
li, err := c.LastImages(ctx, cameraID) // latest picture and snapshot URLs, without waking the camera
err := c.SetCameraOn(ctx, cameraID, false) // DeviceState with On follows; off, the camera does nothing
```

`Run` blocks. It logs in, connects to Arlo's MQTT broker, pings the base
stations every minute, asks them for their devices' state every ten minutes,
renews the two-hour token before it expires, and reconnects with a backoff.
The handler is called from `Run`'s goroutine, one event at a time.
Commands (`SetMode`, `Library`, …) execute on that same goroutine once `Run`
is connected: they wait through reconnections, so bound them with `ctx`, as
`Run` may be in a backoff of up to an hour. They return `arlo.ErrNotRunning`
when `Run` is not running or stops meanwhile. A second `Run` of the same
client returns `arlo.ErrAlreadyRunning`.

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
arlo library (same flags) [-days 7] [-urls]
arlo snapshot (same flags) <camera id>
arlo stream (same flags) [-play mpv] <camera id>
arlo lastimage (same flags) [-o newest.jpg] <camera id>
```

Without `-imap-user` the code is typed on stdin. Responses and MQTT messages
are dumped, secrets redacted, to `-dump` (default `debug/`, empty to
disable): debug from the dumps rather than by spending auth attempts.

## Acknowledgements

[pyaarlo](https://github.com/twrecked/pyaarlo), by Steve Herrell
([twrecked](https://github.com/twrecked)), maps much of Arlo's undocumented
protocol; go-arlo cross-checked its own observations against it. go-arlo
contains no pyaarlo code.

## License

MIT, see `LICENSE`.
