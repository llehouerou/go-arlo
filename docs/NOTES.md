# go-arlo notes

The decisions behind go-arlo and what Arlo was observed to do, with dates:
the API is undocumented and changes. go-arlo was written so Oiko, a home
automation platform, can watch and control Arlo cameras without Home
Assistant; the library itself knows nothing of Oiko.

## Scope

- Per camera: connected, battery level. Integration reachability.
- Location mode: read, set `armHome` / `standby` (`armAway` for free).
- Nice to have: motion per camera.
- Media: recordings library, latest pictures, snapshot on demand, live
  stream URL.
- Out: sirens, base station modes, custom (UUID) location modes — an active
  custom mode is reported raw.

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
| Production host | session at `/var/lib/oiko/arlo-session.json`; secrets from sops-nix via systemd `LoadCredential` (done in Oiko/infrastructure, not here) |

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

## Events and devices results (2026-09-30, production host)

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
  transIds: once per subscribed client, see "Repeated packets" below).
  Harmless for a state.
  Motion packets also carry a `streamURL` with an ingress token: redacted.

## Concurrent sessions (2026-09-30, production host)

With a `watch` running, none of these disturbed it: a second process
reusing the token (REST), a second MQTT connection on the same token (both
received the motion events), a fresh `/api/auth` elsewhere (the old token
kept working, no `logout`). Arlo tolerates concurrent sessions of this
account; the `logout` pyaarlo warns about must come from something else.

## Modes results (2026-09-30, production host)

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

## Token renewal and trust rotation (2026-09-30, production host)

- The background `watch` renewed its token at 17:36:38 as planned (expiry
  minus 10 min) with no `Connection` flap, and got a fresh 2 h token.
- It needed an email 2FA though (handled by IMAP in 7 s, then re-paired):
  `getFactorId` answered 9261. Cause: a trusted `startAuth` **rotates the
  `browser_trust` cookie** and voids the previous one, and the concurrency
  test had logged in from a copy of the session file.
- Rule: one owner per session file, never a copy, never the same file on
  two hosts. The session is now saved right after the auth succeeds, before
  `validateAccessToken`, so a failure there does not lose the new cookie.
- Confirmed at 19:26: with a single owner, the renewal went through the
  trusted browser, no 2FA.

## Repeated packets (2026-10-01, production host, Oiko with dumps on)

- A base station sends each spontaneous packet (`cameras/<id>` with
  `motionDetected`, and the property-only ones around it) **once per client
  subscribed to it**. Motion with only Oiko subscribed: `true` ×1, `false`
  ×1. Same motion with the Arlo app open on an Android phone: ×2 each.
- The copies have distinct `transId`s and **no `to`** (null): nothing tells
  them apart. Only answers to a request (`subscriptions/…/is`,
  `devices/is`) carry `to`, set to the requester (`<userId>_web` for us).
- Any account sharing the base counts. Copies of the 08:28 motion: `true`
  ×3 (Oiko, the owner's Android app, and an Android app on go-arlo's account just
  logged out), `false` ×2 twenty seconds later.
- An app counts while open: it subscribes on opening, then every 20 s
  (`subscriptions/<id>`, transIds `and!…`). A subscription outlives its last
  renewal by about two minutes (the logged-out app, last renewed 08:26:17,
  was gone between 08:28:15 and 08:28:31). Push notifications to a closed
  app are not subscriptions.
- Answers to the app's own requests also reach every subscriber, once per
  subscriber, with the same `transId` (presumably `publishResponse: true`):
  their multiplicity counts the live subscriptions. Answers to ours
  (`publishResponse: false`) come once.
- Hence events are states that may repeat; go-arlo passes the repeats on
  and Oiko records an identical value as a refresh, not a change.
- The Android app was first logged into this granted-access account (it
  subscribes as `<userId>`, without `_web`), then into the owner's
  (`<ownerId>`).

## Dev account and media library (2026-10-03, dev machine)

- A third account, a dev one, is granted access by the owner for
  development, so nothing done from a dev machine touches Oiko's account,
  its trust cookie or its auth rate limit. Its session lives outside the
  repo, on the dev machine only. Logging it in and running `watch` did not
  disturb Oiko on the production host.
- A granted-access account reads the owner's library:
  `POST /hmsweb/users/library` `{dateFrom, dateTo: YYYYMMDD}` → 143 entries
  over 7 days, newest first, all from camera A (the camera armed in
  standby). Each has `deviceId` (bare
  serial), `utcCreatedDate` (epoch ms), `mediaDurationSecond`,
  `contentType` `video/mp4`, `reason` `motionRecord`, `ownerId`,
  `presignedContentUrl` and `presignedThumbnailUrl` on
  `arlos3-prod-z1.arlo.com`, `meta` (width, height, bit rate). No
  `objCategory`: no smart detection on this plan.
- A plain GET of the presigned URLs, without auth, returns the MP4 and a
  640×357 JPEG thumbnail. They expire 24 h after the listing.
- Presigned URLs are redacted from dumps: anyone holding one gets the
  media.

## Snapshot on demand (2026-10-03, dev machine, camera B)

- `POST /hmsweb/users/devices/fullFrameSnapshot` (header `xcloudId`) with a
  notify body: `{to: baseId, from: <userId>_web, transId, action: set,
  resource: cameras/<id>, publishResponse: true, properties:
  {activityState: fullFrameSnapshot}}` → `{success: true}` at once. A
  granted-access account may ask.
- The camera then reports `activityState` `fullFrameSnapshot`, then `idle`
  4–7 s later, and the base sends `action: fullFrameSnapshotAvailable`,
  resource `cameras/<id>`, `properties.presignedFullFrameSnapshotUrl`:
  4–8 s after the request in two tries. Like every spontaneous packet, it
  reaches every subscriber, once each: whoever asked, every client gets
  `SnapshotReady`.
- The URL (same `arlos3-prod-z1` host, 24 h) serves a 1920×1072 JPEG,
  ~250 kB, as `binary/octet-stream`.

## Live stream (2026-10-03, dev machine, camera B)

- `POST /hmsweb/users/devices/startStream` (header `xcloudId`), notify body
  with `responseUrl: ""` and `properties: {activityState: startUserStream,
  cameraId}` → at once `{url, bandwidthTestUrl, nextgenServer: false}`.
  `url` is `rtsp://<IP>:443/vzmodulelive/<camId>_<ms>?egressToken=…&
  userAgent=arloMobileClient&dType=iOS…`: RTSP over TLS, so `rtsps://`
  (the iOS user agent picks RTSP). A granted-access account may stream.
- The certificate does not match the IP: clients must skip verification
  (`ffmpeg -tls_verify 0 -rtsp_transport tcp -i …`). mpv plays it as is
  (`--tls-verify` defaults to no); no desktop handler takes `rtsps://`, so
  `arlo stream -play` runs a given player rather than `xdg-open`.
- ffmpeg read H.264 1920×1072 and AAC 16 kHz mono within ~3 s.
- The camera reports `activityState` `userStreamActive` (with the
  `streamURL`) at start and `idle` one second after the reader leaves: no
  stop request is needed. pyaarlo's stop (`activityState: idle`) is not
  ported.
- Battery 81 % → 78 % over two snapshots and a ~10 s stream.

## Latest pictures without waking a camera (2026-10-03, dev machine)

- `/hmsweb/v2/users/devices` carries, per camera, presigned URLs valid
  24 h from the listing, at fixed paths per camera:
  - `presignedLastImageUrl`: 640×357 JPEG, replaced after each recording
    (it is then the recording's `_thumb.jpg`) and after each live stream
    (`lastImage.jpg`, ~50 s after the stream started).
  - `presignedFullFrameSnapshotUrl`: 1920×1072 JPEG, the latest
    full-frame snapshot (`fullFrameSnapshot.jpg`); camera A's dated from
    August.
  - `presignedSnapshotUrl`: 404.
  - `lastImageUploaded`: a bool.
- Only the GET's `Last-Modified` dates a picture; reading the list does not
  touch the camera.
- `LastImages` answers from memory since 2026-10-06: `Run` keeps both URLs
  from the device list it reads on each connection and token renewal
  (under 2 h apart, far within the 24 h), and takes the snapshot URL from
  `SnapshotReady`.

## Live view limits (2026-10-06, dev machine, camera B)

- **No session limit seen**: one reader (`ffmpeg … -f null -`) read a
  stream for 11 min 38 s until it was killed; Arlo never ended it, no
  error, no `activityState` change meanwhile. The camera went `idle` ~1 s
  after the reader left. Battery 78 % → 76 % over that stream and its
  start (77 % at start, 76 % one minute after the end).
- **A second `startStream` while the stream runs joins it**: same path
  (`/vzmodulelive/<camId>_<ms>`, the `<ms>` of the first start), a new
  `egressToken`, answered in ~270 ms. Both URLs were read at once by two
  readers, the first without a gap; the second's timestamps continue the
  first's (one stream, two egresses). A third call 2 min later, the first
  reader still on, got the same path again. The camera reported
  `userStreamActive` again (with a `streamURL`) for each call, `idle` only
  once the last reader left.
- **Timing**: from an idle camera, the first key frame reached the reader
  ~5.5 s after the `startStream` call (the URL itself comes in ~0.3 s;
  ffprobe with `-analyzeduration 0 -probesize 32`). A reader joining a
  running stream got its first key frame ~4 s after its call: no cached
  GOP, it waits for the next key frame. Key frames every **2.0 s**
  (48 frames at 24 fps, 40 or 24 when the frame rate drops at night).
- **Codecs** (ffprobe): video H.264 High, level 4.0 (SPS `67 64 00 28`),
  1920×1072, `yuvj420p`, ~24 fps variable (`r_frame_rate` 289/12); audio
  AAC LC, 16 kHz, mono (`AudioSpecificConfig` `14 08`). Browser codecs:
  `avc1.640028, mp4a.40.2`. ffmpeg logs a few non-monotonic video DTS
  (78 over 11 min), harmless to a null muxer.
- **Portable URLs**: a URL obtained on the dev machine was read from the
  production host (ffprobe with the URL only, no session copied): the
  egress is not bound to the requester's IP.

## New recordings (2026-10-06, dev machine, camera A)

- A granted-access account receives the library notices: subscribed to
  `u/<userId>/in/library/{add,update}` (pyaarlo also subscribes to
  `remove`, and parses none of them: it reloads the library on
  `mediaUploadNotification` / `recordingStopped`).
- Each motion recording brings two messages on `library/add`, about a
  second apart, at the end of its upload: resource
  `mediaUploadNotification`, no `action`, with `deviceId`, `ownerId`,
  `uniqueId` (`<ownerId>_<camId>`), `createdDate` (`YYYYMMDD`) and
  `mediaObjectCount` (the day's count). The first carries only
  `presignedLastImageUrl`; the second also `presignedContentUrl`,
  `presignedThumbnailUrl` and `recordingStopped: true`. Nothing came on
  `update`, and no repeat, over two recordings.
- The notice lacks the library's `utcCreatedDate`,
  `mediaDurationSecond`, `reason` and `objCategory`, but the object is
  named after `utcCreatedDate`: `…/<camId>/recordings/<epoch ms>.mp4` and
  `<epoch ms>_thumb.jpg`, the same object the library lists.
- Timing: recordings of 18 s and 20 s, notices 24 s and 33 s after their
  start (6 s and 14 s after their end), within a second of the
  `motionDetected: false` and `idle` packets. The base's packets lag the
  same way: `motionDetected: true` came 7 s and 14 s after the start
  (`alertStreamActive` `dateStarted` ≈ `utcCreatedDate`), so the base
  seems to send its camera packets late while it uploads.
- The notice's URLs read at once: GET right on receipt gave the MP4
  (1.2–1.3 MB, `video/mp4`) and the 640×357 thumbnail, both
  `Last-Modified` the second the notice was sent. `POST /library` listed
  the entry 5 s after the notice (first poll).
- `Run` turns the second message into `RecordingAdded`; a recording made
  while disconnected is not replayed.

## Not yet ported from pyaarlo (roadmap, 2026-09-30)

What pyaarlo does that go-arlo does not, ranked by use for a home automation
host. Left out: doorbells, lights, sensors, audio playback, nightlight and
flood/spotlights (no such hardware here).

1. **Free: the data already arrives.** The base's answer to `get devices`
   (every ten minutes) also carries, per camera: `signalStrength`,
   `chargingState` / `chargerTech` / `batteryTech`, `activityState`
   (`idle`, `alertStreamActive`, `userStreamActive`), `audioDetected` (a
   second trigger), `privacyActive` / `cameraOff`, `swVersion` /
   `updateAvailable` (base too). Only `DeviceState` needs more fields.
2. **Base siren** (`base.py` `siren_on(duration, volume)`, `siren_off`):
   notify `{action: set, resource: siren, publishResponse: true,
   properties: {sirenState: on|off, duration, volume (1-8), pattern:
   alarm}}`. The VMB4000 has one (pyaarlo: models `VMB400*`, `VMB450*`).
   Makes a real alarm out of other sensors.
3. ~~**New recording events**~~: done 2026-10-06, `RecordingAdded` (see
   "New recordings").
4. **Camera on/off** (`camera.py` `turn_on`/`turn_off`): notify
   `{action: set, resource: cameras/<id>, publishResponse: true,
   properties: {privacyActive: bool}}`. Privacy while someone is home.
5. **Base restart** (`POST /hmsweb/users/devices/restart` `{deviceId}`):
   a remedy for a watchdog, while the base still reaches the cloud.

Low value: motion sensitivity settings (set once in the app), custom modes
and schedules (pyaarlo does not handle V3 schedules; the owner's run on
Arlo's side), SSE fallback (only if `mqttUrl` turns `wss`), RATLS local
storage access.
