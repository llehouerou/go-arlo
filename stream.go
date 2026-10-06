package arlo

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// stream is what one connection to the event stream knows: its base
// stations, whether each answers pings, the account's Location and each
// camera's latest pictures. It turns what the connection receives into
// events and does no I/O. Only Run's goroutine holds it.
type stream struct {
	bases   []device
	topics  []string // to subscribe to, sorted
	devices Devices
	baseUp  map[string]bool
	loc     location              // zero until located
	images  map[string]LastImages // per camera
}

// newStream reads the account's device list.
func newStream(userID string, devs []device) *stream {
	s := &stream{
		topics: []string{
			"u/" + userID + "/in/userSession/connect",
			"u/" + userID + "/in/userSession/disconnect",
		},
		baseUp: map[string]bool{},
		images: map[string]LastImages{},
	}
	for _, d := range devs {
		s.topics = append(s.topics, d.Topics...)
		// The device list also holds pseudo devices, like a base's siren
		// under the base's own id.
		if d.Type != "basestation" && d.Type != "camera" {
			continue
		}
		if d.Type == "basestation" {
			s.bases = append(s.bases, d)
		} else {
			s.images[d.ID] = LastImages{Image: d.LastImageURL, Snapshot: d.SnapshotURL}
		}
		dev := Device{ID: d.ID, Name: d.Name, Model: d.Model, Type: d.Type}
		if d.ParentID != d.ID {
			dev.BaseID = d.ParentID
		}
		s.devices = append(s.devices, dev)
	}
	// Each device of a base lists the same topics.
	slices.Sort(s.topics)
	s.topics = slices.Compact(s.topics)
	return s
}

// pinged takes a base station's answer to a ping and reports its presence
// when it changes. A 401 means the token is void: the error ends the
// connection.
func (s *stream) pinged(b device, err error) ([]Event, error) {
	var refused *apiError
	if errors.As(err, &refused) && refused.status == 401 {
		return nil, fmt.Errorf("arlo: ping %s: %w", b.Name, err)
	}
	up := err == nil
	if was, seen := s.baseUp[b.ID]; seen && was == up {
		return nil, nil
	}
	s.baseUp[b.ID] = up
	return []Event{DeviceState{ID: b.ID, Connected: &up}}, nil
}

// received turns an event stream message into events. errLoggedOut ends the
// connection; other errors mean an unreadable message.
func (s *stream) received(payload []byte) ([]Event, error) {
	var msg packet
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	if msg.Action == "logout" {
		return nil, errLoggedOut
	}
	var out []Event
	for _, e := range msg.events() {
		if dm, ok := e.(deviceMode); ok {
			if s.loc.ID == "" || !slices.ContainsFunc(s.bases, func(b device) bool { return b.ID == dm.ID }) {
				continue
			}
			e = s.modeChanged(dm.Mode)
		}
		if sr, ok := e.(SnapshotReady); ok {
			if li, known := s.images[sr.ID]; known {
				li.Snapshot = sr.URL
				s.images[sr.ID] = li
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// located picks the account's Location among its own and shared ones: the
// one holding its base stations. An account can also see locations of its
// own with no device, and shared locations name their gateways
// "<ownerId>_<deviceId>".
//
// ponytail: one location with bases only (ours); SetMode would need a
// location argument for accounts with several.
func (s *stream) located(own, shared []location) error {
	var found []location
	for _, l := range append(own, shared...) {
		if slices.ContainsFunc(l.Gateways, func(g string) bool {
			return slices.ContainsFunc(s.bases, func(b device) bool { return g == b.ID || strings.HasSuffix(g, "_"+b.ID) })
		}) {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		return fmt.Errorf("locations: %d hold a base station, only one is supported", len(found))
	}
	s.loc = found[0]
	return nil
}

// modeChanged reports the Location's mode, read or set.
func (s *stream) modeChanged(m Mode) ModeChanged {
	return ModeChanged{LocationID: s.loc.ID, LocationName: s.loc.Name, Mode: m}
}

// Device is a base station or a camera of the account.
type Device struct {
	ID     string // serial number
	Name   string
	Model  string
	Type   string // "basestation" or "camera"
	BaseID string // parent base station; empty for a base station
}

// Devices lists the account's devices. Run reports it on every connection.
type Devices []Device

// DeviceState is what Arlo reports about a device; nil fields were not part
// of the report.
type DeviceState struct {
	ID        string
	Connected *bool
	Battery   *int // percent
}

// Motion reports whether a camera sees motion: on connection, then as it
// starts or stops.
type Motion struct {
	ID     string
	Active bool
}

// deviceMode is a device's report of the mode it applies. The stream turns a
// base station's into ModeChanged for its location; it never reaches handlers.
type deviceMode struct {
	ID   string
	Mode Mode
}

func (deviceMode) isEvent()  {}
func (Devices) isEvent()     {}
func (DeviceState) isEvent() {}
func (Motion) isEvent()      {}

// flexBool reads Arlo's booleans, sent either as JSON booleans or as the
// strings "True" and "False".
type flexBool bool

func (b *flexBool) UnmarshalJSON(d []byte) error {
	*b = flexBool(strings.EqualFold(strings.Trim(string(d), `"`), "true"))
	return nil
}

type properties struct {
	ConnectionState *string   `json:"connectionState"`
	BatteryLevel    *int      `json:"batteryLevel"`
	MotionDetected  *flexBool `json:"motionDetected"`
	SnapshotURL     *string   `json:"presignedFullFrameSnapshotUrl"`
}

// events are what a device's properties report: its state, and whether it
// sees motion. A base station's answer to "get devices" carries the same
// properties as a camera's own packets, so motion is known from connection.
func (p properties) events(id string) []Event {
	var out []Event
	s := DeviceState{ID: id, Battery: p.BatteryLevel}
	if p.ConnectionState != nil {
		up := *p.ConnectionState == "available"
		s.Connected = &up
	}
	if s.Connected != nil || s.Battery != nil {
		out = append(out, s)
	}
	if p.MotionDetected != nil {
		out = append(out, Motion{ID: id, Active: bool(*p.MotionDetected)})
	}
	if p.SnapshotURL != nil {
		out = append(out, SnapshotReady{ID: id, URL: *p.SnapshotURL})
	}
	return out
}

// packet is the part of an event stream message we read. properties and
// devices vary in shape between packets, so they are decoded on demand.
type packet struct {
	Action     string          `json:"action"`
	Resource   string          `json:"resource"`
	States     json.RawMessage `json:"states"`
	Properties json.RawMessage `json:"properties"`
	Devices    json.RawMessage `json:"devices"`
}

// events turns a packet into events. See pyaarlo's docs/packets.md: a base
// station answers "get devices" with its children's full state (resource
// "devices"), cameras push changes as they happen (resource
// "cameras/<id>") and a mode change shows as every device's
// "devices/<id>/states" with its activeMode. Packets of an unexpected shape
// yield nothing.
func (m packet) events() []Event {
	var out []Event
	switch {
	case m.Resource == "devices":
		var devs map[string]struct {
			Properties properties `json:"properties"`
		}
		if json.Unmarshal(m.Devices, &devs) != nil {
			return nil
		}
		for _, id := range slices.Sorted(maps.Keys(devs)) {
			out = append(out, devs[id].Properties.events(id)...)
		}
	case strings.HasPrefix(m.Resource, "cameras/"):
		var p properties
		if json.Unmarshal(m.Properties, &p) != nil {
			return nil
		}
		id, _, _ := strings.Cut(strings.TrimPrefix(m.Resource, "cameras/"), "/")
		out = p.events(id)
	case strings.HasPrefix(m.Resource, "devices/") && strings.HasSuffix(m.Resource, "/states"):
		var s struct {
			ActiveMode Mode `json:"activeMode"`
		}
		if json.Unmarshal(m.States, &s) != nil || s.ActiveMode == "" {
			return nil
		}
		id := strings.TrimSuffix(strings.TrimPrefix(m.Resource, "devices/"), "/states")
		out = append(out, deviceMode{ID: id, Mode: s.ActiveMode})
	}
	return out
}
