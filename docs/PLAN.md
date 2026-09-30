# go-arlo plan

A native Go client for Arlo's cloud API, ported from pyaarlo 0.8.0.23, so Oiko
can watch and control the Arlo cameras without Home Assistant. Plain Go
library plus a small CLI; Oiko-agnostic.

## Scope

- Per camera: connected, battery level. Integration reachability.
- Location mode: read, set `armHome` / `standby` (`armAway` for free).
- Nice to have: motion per camera.
- Out: video, streams, snapshots, library, sirens, base station modes, custom
  (UUID) location modes — an active custom mode is reported raw.

## Decisions

| Topic | Choice |
|---|---|
| Module | `github.com/llehouerou/go-arlo`, flake devShell with `go_1_27` |
| Account | `a dedicated Arlo account`; HA's aarlo integration is stopped while testing (one account, one session) |
| HTTP | `github.com/imroc/req/v3` with `ImpersonateChrome()`; Arlo iOS user agent like pyaarlo |
| MQTT | `github.com/eclipse/paho.golang` (v5, already in Oiko). Unproven against Arlo's broker: checked in the login spike, fall back to `eclipse/paho.mqtt.golang` (3.1.1, what pyaarlo speaks) if refused |
| IMAP | `github.com/emersion/go-imap/v2` |
| 2FA code | injected `func(ctx) (string, error)`; IMAP and stdin (CLI) implementations |
| Session | JSON file at a caller-given path, 0600, atomic write: user device id, browser auth code, cookies, token, expiry |
| API | `Client.Run(ctx, func(Event)) error` blocks and owns login, MQTT and reconnects; commands are methods (`SetMode`) on the session `Run` holds |
| Events | concrete types: `Connection`, `DeviceState` (serial, connected, battery), `Motion`, `ModeChanged` |
| the production host | session at `/var/lib/oiko/arlo-session.json`; secrets from sops-nix via systemd `LoadCredential` (done in Oiko/infrastructure, not here) |

## Arlo's auth rate limit

Authentication attempts are rate limited with a long cooldown. Hence:

- One auth attempt per command, never an automatic retry (pyaarlo retries 3x).
- On start, validate the saved token (`/api/validateAccessToken`) and only call
  `/api/auth` when it is expired or rejected. pyaarlo always re-authenticates.
- `Run` re-logs in only on logout/401, with a backoff floor of minutes.
- Every auth response is dumped, redacted, to a debug directory: debug from the
  dumps, not by retrying. The dumps become test fixtures.
- the production host gets the session file made on the dev machine (same public IP; trust is the
  device id + cookies) instead of a second pairing.

## What pyaarlo does (verified in the source)

Login (`backend.py`), all on `https://ocapi-app.arlo.com`:

1. `POST /api/auth` `{email, password: base64, language: "en", EnvSource: "prod"}`
   with `X-User-Device-Id` (persistent UUID), `X-User-Device-Type: BROWSER`,
   `X-Service-Version: 3`, Origin/Referer `https://my.arlo.com`.
2. If `authCompleted` is false, with `Authorization: base64(token)`:
   - `POST /api/getFactorId` `{factorType: BROWSER, factorData: "", userId}`
     with the saved cookies. 200 → trusted browser: `POST /api/startAuth`
     with that factor, done.
   - otherwise `GET /api/getFactors`, pick the EMAIL factor, snapshot the IMAP
     inbox, `POST /api/startAuth` `{factorId, factorType: "BROWSER", userId}`,
     read the 6-digit code from a new `do_not_reply@arlo.com` mail,
     `POST /api/finishAuth` `{factorAuthCode, otp, isBrowserTrusted: true}`.
3. `GET /api/validateAccessToken`.
4. If paired by email: `POST /api/startPairingFactor`
   `{factorAuthCode: browserAuthCode, factorData: "", factorType: BROWSER}`,
   save cookies.
5. `GET https://myapi.arlo.com/hmsweb/users/session/v3` → `mqttUrl`,
   `supportsMultiLocation`.

API calls add `Authorization: <token>`, `Auth-Version: 2`, `SchemaVersion: 1`,
`x-transaction-id` and `?eventId=…&time=…`.

Events: MQTT over TCP+TLS at `mqttUrl` (a `wss` URL means SSE instead).
Client id `user_<userId>_` + 10 random digits, username userId, password token.
Topics `u/<userId>/in/userSession/{connect,disconnect}` and each device's
`allowedMqttTopics`. `{"action":"logout"}` means the session was taken.

Devices: `GET /hmsweb/v2/users/devices`. The VMB4000 is an "old style" base:
child state (battery, connection) comes from
`POST /hmsweb/users/devices/notify/<baseId>` (header `xcloudId`)
`{action: get, resource: devices, publishResponse: false}`, answered **on the
event stream**. Base presence: notify `{action: set, resource:
subscriptions/<userId>_web, properties: {devices: [baseId]}}` every 60 s;
failure means unavailable.

Modes (V3, `location.py`, when `supportsMultiLocation`):
`GET /hmsdevicemanagement/users/<userId>/locations`,
`GET /hmsweb/automation/v3/activeMode?locationId=<id>` → `{properties: {mode},
revision}`, `PUT` same URL `&revision=<rev>` `{mode: armHome}`, headers
`x-forwarded-user` and `x-user-device-id` = userId. Changes arrive as resource
`automation/activeMode`.

## Layers

Each layer works end to end before the next, and leaves one runnable check.

0. **Skeleton**: go.mod, flake devShell, `cmd/arlo`.
1. **Login spike**
   - a. `arlo probe`: Cloudflare check without credentials, impersonated vs
     plain `net/http`. Zero auth attempts.
   - b. `arlo login`: full flow above, IMAP or stdin code, session saved.
   - c. Rerun: from the session, no 2FA, no `/api/auth` while the token is valid.
   - d. One MQTT v5 CONNECT to `mqttUrl`: settles the MQTT library.
   - e. Static binary + session copied to the production host, `arlo login` needs no 2FA.
   - Check: `httptest` replay of the recorded auth responses.
2. **Events**: `Run`, subscriptions, base ping, reconnect; `Connection` events;
   `arlo watch`. Check: dispatcher over recorded payloads.
3. **Devices**: devices list + `get devices` notify at start and every 10 min;
   `DeviceState` (and `Motion` if the same packets carry it). Check: recorded
   payloads.
4. **Modes**: locations, active mode, `SetMode` (GET revision, PUT),
   `ModeChanged`; `arlo mode [set armHome|standby]`. Check: `httptest`.

Then, outside this repo: the `home.Port` adapter in Oiko, `*File` options in
Oiko's NixOS module, sops secrets on the production host.
