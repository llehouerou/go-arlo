package arlo

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

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

// Motion reports motion starting or stopping in front of a camera.
type Motion struct {
	ID     string
	Active bool
}

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
}

func (p properties) state(id string) (DeviceState, bool) {
	s := DeviceState{ID: id, Battery: p.BatteryLevel}
	if p.ConnectionState != nil {
		up := *p.ConnectionState == "available"
		s.Connected = &up
	}
	return s, s.Connected != nil || s.Battery != nil
}

// packet is the part of an event stream message we read. properties and
// devices vary in shape between packets, so they are decoded on demand.
type packet struct {
	Action     string          `json:"action"`
	Resource   string          `json:"resource"`
	Properties json.RawMessage `json:"properties"`
	Devices    json.RawMessage `json:"devices"`
}

// events turns a packet into events. See pyaarlo's docs/packets.md: a base
// station answers "get devices" with its children's full state (resource
// "devices") and cameras push changes as they happen (resource
// "cameras/<id>"). Packets of an unexpected shape yield nothing.
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
			if s, ok := devs[id].Properties.state(id); ok {
				out = append(out, s)
			}
		}
	case strings.HasPrefix(m.Resource, "cameras/"):
		var p properties
		if json.Unmarshal(m.Properties, &p) != nil {
			return nil
		}
		id, _, _ := strings.Cut(strings.TrimPrefix(m.Resource, "cameras/"), "/")
		if s, ok := p.state(id); ok {
			out = append(out, s)
		}
		if p.MotionDetected != nil {
			out = append(out, Motion{ID: id, Active: bool(*p.MotionDetected)})
		}
	}
	return out
}
