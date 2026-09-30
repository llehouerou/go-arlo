# go-arlo notes

The decisions behind go-arlo and what Arlo was observed to do, with dates:
the API is undocumented and changes. go-arlo was written so Oiko, a home
automation platform, can watch and control Arlo cameras without Home
Assistant; the library itself knows nothing of Oiko.

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
| Account | a dedicated granted-access account; its Home Assistant integration was stopped while testing |
| HTTP | `github.com/imroc/req/v3` with `ImpersonateChrome()`; Arlo iOS user agent like pyaarlo |
| MQTT | `github.com/eclipse/paho.golang` (v5, already in Oiko), proven against Arlo's broker in the login spike |
| IMAP | `github.com/emersion/go-imap/v2` |
| 2FA code | injected `func(ctx, since time.Time) (string, error)`; IMAP (newest `do_not_reply@arlo.com` mail after `since`) and stdin (CLI) implementations |
| Session | JSON file at a caller-given path, 0600, atomic write: user device id, browser auth code, cookies, token, expiry |
| API | `Client.Run(ctx, func(Event)) error` blocks and owns login, MQTT and reconnects; commands are methods (`SetMode`) on the session `Run` holds |
| Events | concrete types: `Connection`, `Devices`, `DeviceState` (serial, connected, battery), `Motion`, `ModeChanged` |
| the production host | session at `/var/lib/oiko/arlo-session.json`; secrets from sops-nix via systemd `LoadCredential` (done in Oiko/infrastructure, not here) |

## Arlo's auth rate limit

Authentication attempts are rate limited with a long cooldown. Hence:

- One auth attempt per command, never an automatic retry (pyaarlo retries 3x).
- On start, validate the saved token (`/api/validateAccessToken`) and only call
  `/api/auth` when it is expired or rejected. pyaarlo always re-authenticates.
- `Run` re-logs in only on logout/401, with a backoff floor of minutes.
- Every auth response is dumped, redacted, to a debug directory: debug from the
  dumps, not by retrying. The dumps become test fixtures.
- One trusted session per place: the spike paired on the production host directly, and the
  session file there is the one to hand to Oiko, not a second pairing.

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

## Login spike results (2026-09-30, run on the production host)

- Cloudflare let the impersonated client through on every call, `/api/auth`
  included. Credential-free probes pass with plain `net/http` too.
- Full email 2FA worked first time: IMAP read the code from the mailbox in ~10 s,
  then `startPairingFactor` set our own `browser_trust_<accountId>` cookie.
- Rerun with the saved token: `validateAccessToken` + `session/v3` only.
- Rerun without token: one `/api/auth`, `getFactorId` accepted the trust
  cookie, no 2FA.
- Tokens live **2 hours** (`expiresIn` is an epoch, issue time + 7200 s).
  Token reuse only spares auths across quick restarts; `Run` will need a
  trusted-browser re-auth about every 2 hours.
- An untrusted browser gets HTTP 200 with `meta.code` 400, error 9261
  "Invalid factor data" from `getFactorId`. HA's own trust cookie was refused
  that way when replayed with HA's device id.
- `mqttUrl` is `ssl://mqtt-cluster-v2-z1-1.arloxcld.com:443`; MQTT v5 over
  TLS with paho.golang: CONNACK 0, SUBACK granted. paho.golang stays.
- `supportsMultiLocation` is true: modes are V3, per location.
- HA's aarlo config entry is disabled (`disabled_by: user`) on the production host while this
  client owns the account; backup next to `core.config_entries`.

## Events and devices results (2026-09-30, the production host)

- `/v2/users/devices` lists the base, a pseudo `siren` device under the
  base's id, and the two cameras, with no `properties`: state only comes
  from the base's answer to `get devices`, on
  `d/<xCloudId>/out/devices/is`. `batteryLevel` is an int,
  `motionDetected` a JSON bool, `connectionState` `available`.
- The 4 devices share the same 16 allowed topics (19 with the user session
  ones after dedup).
- Each ping is answered on `d/<xCloudId>/out/subscriptions/<us>_web/is`.
  Another client (`<ownerId>_web`, not this account) also subscribes every
  ~30 s: probably the Arlo app of an account the base is shared with.
- First readings: base connected, camera A 31 %, camera B 81 %.
- Motion (armed camera A): `cameras/<id>` packets with `motionDetected`
  true then false ~6 s later, each sent twice by the base (distinct
  transIds, likely once per subscribed client). Harmless for a state.
  Motion packets also carry a `streamURL` with an ingress token: redacted.

## Concurrent sessions (2026-09-30, the production host)

With a `watch` running, none of these disturbed it: a second process
reusing the token (REST), a second MQTT connection on the same token (both
received the motion events), a fresh `/api/auth` elsewhere (the old token
kept working, no `logout`). Arlo tolerates concurrent sessions of this
account; the `logout` pyaarlo warns about must come from something else.

## Modes results (2026-09-30, the production host)

- The account go-arlo uses is a **granted access** account. Its locations: an empty
  `Home` of its own (no gateway) and the owner's shared `Home`, whose
  `gatewayDeviceIds` are `<ownerId>_<deviceId>`. The client picks the
  location holding a base, like pyaarlo. The other subscriber `<ownerId>` is
  the owner.
- A granted-access account can read and set the mode. `activeMode` answers
  `{properties: {mode}, revision, source}`; `source` was `schedule`: the
  owner has an Arlo schedule.
- No `automation/activeMode` packet arrives on our topics. A mode change
  shows as `devices/<id>/states` with `states.activeMode` for the base and
  each camera; the base's is turned into `ModeChanged`. The mode is also
  read at connection and every ten minutes.
- `standby` still holds motion rules for camera A: that is the camera
  "armed all the time".
- armHome then standby round trip done twice, location left in standby.

## Token renewal and trust rotation (2026-09-30, the production host)

- The background `watch` renewed its token at 17:36:38 as planned (expiry
  minus 10 min) with no `Connection` flap, and got a fresh 2 h token.
- It needed an email 2FA though (handled by IMAP in 7 s, then re-paired):
  `getFactorId` answered 9261. Cause: a trusted `startAuth` **rotates the
  `browser_trust` cookie** and voids the previous one, and the concurrency
  test had logged in from a copy of the session file.
- Rule: one owner per session file, never a copy, never the same file on
  two hosts. The session is now saved right after the auth succeeds, before
  `validateAccessToken`, so a failure there does not lose the new cookie.
