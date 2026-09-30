package arlo

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

// Packets from pyaarlo's docs/packets.md, trimmed.
func TestPacketEvents(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"subscription reply", `{"action":"is","from":"B","properties":{"devices":["B"]},"resource":"subscriptions/U_web"}`, nil},
		{"v2 base mode change", `{"B":{"activeModes":["mode1"]},"resource":"activeAutomations"}`, nil},
		{"motion", `{"action":"is","from":"B","properties":{"motionDetected":"True"},"resource":"cameras/C1"}`,
			[]string{"motion C1 true"}},
		{"motion as bool, stop", `{"action":"is","properties":{"motionDetected":false},"resource":"cameras/C1"}`,
			[]string{"motion C1 false"}},
		{"camera battery", `{"action":"is","properties":{"batteryLevel":12,"connectionState":"unavailable"},"resource":"cameras/C2"}`,
			[]string{"state C2 connected=false battery=12"}},
		{"base answer to get devices", `{"action":"is","resource":"devices","from":"B","devices":{
			"C2":{"properties":{"batteryLevel":45,"connectionState":"available","motionDetected":"False"},"states":{}},
			"C1":{"properties":{"connectionState":"thermalShutdownCold"}},
			"B":{"properties":{"connectivity":[{"connected":"True"}],"state":"idle"},"states":{}}}}`,
			[]string{"state C1 connected=false battery=-", "state C2 connected=true battery=45"}},
		// Real VMB4000 packet after a SetMode, trimmed.
		{"mode change", `{"action":"is","from":"B","resource":"devices/B/states","states":{"activeMode":"armHome","schemaVersion":1,"source":"client-U"}}`,
			[]string{"device mode B armHome"}},
		{"device states without mode", `{"action":"is","resource":"devices/C1/states","states":{"schemaVersion":1}}`, nil},
		{"properties as a list", `{"resource":"cameras/C1","properties":[{"serialNumber":"C1"}]}`, nil},
	}
	for _, tc := range cases {
		var p packet
		if err := json.Unmarshal([]byte(tc.raw), &p); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var got []string
		for _, e := range p.events() {
			got = append(got, describe(e))
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func describe(e Event) string {
	switch e := e.(type) {
	case Motion:
		return fmt.Sprintf("motion %s %v", e.ID, e.Active)
	case deviceMode:
		return fmt.Sprintf("device mode %s %s", e.ID, e.Mode)
	case DeviceState:
		conn, batt := "-", "-"
		if e.Connected != nil {
			conn = fmt.Sprint(*e.Connected)
		}
		if e.Battery != nil {
			batt = fmt.Sprint(*e.Battery)
		}
		return fmt.Sprintf("state %s connected=%s battery=%s", e.ID, conn, batt)
	}
	return fmt.Sprintf("%#v", e)
}
