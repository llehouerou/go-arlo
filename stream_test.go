package arlo

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The account of the production tests: base B with its siren pseudo device, and
// cameras C1, C2. Its location is shared by the owner.
func testStream(t *testing.T, located bool) *stream {
	t.Helper()
	topics := []string{"d/X/out/cameras/#", "d/X/out/devices/#"}
	st := newStream("U", []device{
		{ID: "B", Type: "basestation", Name: "Base", ParentID: "B", Topics: topics},
		{ID: "B", Type: "siren", ParentID: "B", Topics: topics},
		{ID: "C1", Type: "camera", Name: "Gate", ParentID: "B", Topics: topics},
		{ID: "C2", Type: "camera", Name: "Porch", ParentID: "B", Topics: topics},
	})
	if located {
		if err := st.located(
			[]location{{ID: "own", Name: "Home"}},
			[]location{{ID: "L1", Name: "Home", Gateways: []string{"OWNER_B"}}},
		); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// Packets from pyaarlo's docs/packets.md and the production host, trimmed.
func TestStreamReceived(t *testing.T) {
	modeChange := `{"action":"is","from":"B","resource":"devices/B/states","states":{"activeMode":"armHome","schemaVersion":1,"source":"client-U"}}`
	cases := []struct {
		name    string
		located bool
		raw     string
		want    []string
	}{
		{"subscription reply", true, `{"action":"is","from":"B","properties":{"devices":["B"]},"resource":"subscriptions/U_web"}`, nil},
		{"v2 base mode change", true, `{"B":{"activeModes":["mode1"]},"resource":"activeAutomations"}`, nil},
		{"motion", true, `{"action":"is","from":"B","properties":{"motionDetected":"True"},"resource":"cameras/C1"}`,
			[]string{"motion C1 true"}},
		{"motion as bool, stop", true, `{"action":"is","properties":{"motionDetected":false},"resource":"cameras/C1"}`,
			[]string{"motion C1 false"}},
		{"camera battery", true, `{"action":"is","properties":{"batteryLevel":12,"connectionState":"unavailable"},"resource":"cameras/C2"}`,
			[]string{"state C2 connected=false battery=12"}},
		{"base answer to get devices", true, `{"action":"is","resource":"devices","from":"B","devices":{
			"C2":{"properties":{"batteryLevel":45,"connectionState":"available","motionDetected":"False"},"states":{}},
			"C1":{"properties":{"connectionState":"thermalShutdownCold"}},
			"B":{"properties":{"connectivity":[{"connected":"True"}],"state":"idle"},"states":{}}}}`,
			[]string{"state C1 connected=false battery=-", "state C2 connected=true battery=45", "motion C2 false"}},
		// Real VMB4000 packet after a SetMode, trimmed.
		{"base mode change", true, modeChange, []string{"mode L1 Home armHome"}},
		{"base mode change before the location is known", false, modeChange, nil},
		{"camera mode change", true, `{"action":"is","resource":"devices/C1/states","states":{"activeMode":"armHome"}}`, nil},
		{"device states without mode", true, `{"action":"is","resource":"devices/C1/states","states":{"schemaVersion":1}}`, nil},
		{"properties as a list", true, `{"resource":"cameras/C1","properties":[{"serialNumber":"C1"}]}`, nil},
		// Real VMB4000 packet ~8 s after Snapshot, trimmed.
		{"snapshot", true, `{"action":"fullFrameSnapshotAvailable","from":"B","properties":{"disablePrivacyZones":false,"presignedFullFrameSnapshotUrl":"https://s"},"resource":"cameras/C1"}`,
			[]string{"snapshot C1 https://s"}},
		{"snapshot started", true, `{"action":"is","from":"B","properties":{"activityState":"fullFrameSnapshot"},"resource":"cameras/C1"}`, nil},
	}
	for _, tc := range cases {
		es, err := testStream(t, tc.located).received([]byte(tc.raw))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := describeAll(es); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}

	st := testStream(t, true)
	if _, err := st.received([]byte(`{"action":"logout"}`)); !errors.Is(err, errLoggedOut) {
		t.Errorf("logout: %v", err)
	}
	if _, err := st.received([]byte(`not json`)); err == nil || errors.Is(err, errLoggedOut) {
		t.Errorf("unreadable: %v", err)
	}
}

func describeAll(es []Event) []string {
	var out []string
	for _, e := range es {
		out = append(out, describe(e))
	}
	return out
}

func describe(e Event) string {
	switch e := e.(type) {
	case Connection:
		return map[bool]string{true: "up", false: "down"}[e.Up]
	case Devices:
		ids := []string{"devices"}
		for _, d := range e {
			ids = append(ids, d.ID)
		}
		return strings.Join(ids, " ")
	case Motion:
		return fmt.Sprintf("motion %s %v", e.ID, e.Active)
	case ModeChanged:
		return fmt.Sprintf("mode %s %s %s", e.LocationID, e.LocationName, e.Mode)
	case DeviceState:
		conn, batt := "-", "-"
		if e.Connected != nil {
			conn = fmt.Sprint(*e.Connected)
		}
		if e.Battery != nil {
			batt = fmt.Sprint(*e.Battery)
		}
		return fmt.Sprintf("state %s connected=%s battery=%s", e.ID, conn, batt)
	case SnapshotReady:
		return fmt.Sprintf("snapshot %s %s", e.ID, e.URL)
	}
	return fmt.Sprintf("%#v", e)
}
