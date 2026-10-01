# go-arlo

A Go client for Arlo's undocumented cloud: it watches an account's cameras and
reads and sets its alarm mode, for home automation hosts.

## Language

**Event stream**:
Arlo's push channel for one logged-in connection, over which base stations and
cameras report state, motion and mode changes. What a connection knows (its
base stations, their presence, the Location) lasts as long as the connection.
_Avoid_: MQTT feed, subscription

**Base station**:
The hub cameras pair with; Arlo relays requests to it and it answers on the
Event stream. Present while it answers pings.
_Avoid_: hub, gateway

**Device**:
A base station or a camera of the account. Arlo's device list also holds
pseudo devices (a base's siren) that are not Devices.

**Location**:
Arlo's grouping of base stations that carries the alarm Mode. The account's
Location is the one holding its base stations, its own or shared with it.
_Avoid_: site, home

**Mode**:
A Location's alarm state: standby, armHome, armAway, or a custom mode reported
as Arlo names it.
_Avoid_: alarm state, arming

**Session**:
What lets go-arlo act as the account across restarts: a device id, the
trust cookie that makes Arlo treat it as a trusted browser (no two-factor),
and the token while it is valid. The trust cookie rotates on every
authentication, so one Session per account and place, never a copy.
_Avoid_: credentials, login