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
var (
	testTopics  = []string{"d/X/out/cameras/#", "d/X/out/devices/#"}
	testDevices = []device{
		{ID: "B", Type: "basestation", Name: "Base", ParentID: "B", Topics: testTopics},
		{ID: "B", Type: "siren", ParentID: "B", Topics: testTopics},
		{ID: "C1", Type: "camera", Name: "Gate", ParentID: "B", Topics: testTopics},
		{ID: "C2", Type: "camera", Name: "Porch", ParentID: "B", Topics: testTopics},
	}
	testOwn    = []location{{ID: "own", Name: "Home"}}
	testShared = []location{{ID: "L1", Name: "Home", Gateways: []string{"OWNER_B"}}}
)

func testStream(t *testing.T) *stream {
	t.Helper()
	st, err := newStream("U", testDevices, testOwn, testShared)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestNewStreamUnsupportedLocations(t *testing.T) {
	if _, err := newStream("U", testDevices, testOwn, nil); err == nil {
		t.Error("no location holds the base: no error")
	}
	two := append(slices.Clone(testShared), location{ID: "L2", Gateways: []string{"B"}})
	if _, err := newStream("U", testDevices, testOwn, two); err == nil {
		t.Error("two locations hold the base: no error")
	}
}

// Packets from pyaarlo's docs/packets.md and the production host, trimmed.
func TestStreamReceived(t *testing.T) {
	modeChange := `{"action":"is","from":"B","resource":"devices/B/states","states":{"activeMode":"armHome","schemaVersion":1,"source":"client-U"}}`
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
			[]string{"state C1 connected=false battery=-", "state C2 connected=true battery=45", "motion C2 false"}},
		// Real VMB4000 packet after a SetMode, trimmed.
		{"base mode change", modeChange, []string{"mode L1 Home armHome"}},
		{"camera mode change", `{"action":"is","resource":"devices/C1/states","states":{"activeMode":"armHome"}}`, nil},
		{"device states without mode", `{"action":"is","resource":"devices/C1/states","states":{"schemaVersion":1}}`, nil},
		{"properties as a list", `{"resource":"cameras/C1","properties":[{"serialNumber":"C1"}]}`, nil},
		// Real VMB4000 packet ~8 s after Snapshot, trimmed.
		{"snapshot", `{"action":"fullFrameSnapshotAvailable","from":"B","properties":{"disablePrivacyZones":false,"presignedFullFrameSnapshotUrl":"https://s"},"resource":"cameras/C1"}`,
			[]string{"snapshot C1 https://s"}},
		// Real messages on u/<userId>/in/library/add around a recording,
		// trimmed: the last image first, then the recording.
		{"last image uploaded", `{"createdDate":"20000101","deviceId":"C1","mediaObjectCount":130,"ownerId":"O","presignedLastImageUrl":"https://h/O/C1/lastImage.jpg?s=x","resource":"mediaUploadNotification","uniqueId":"O_C1"}`, nil},
		{"recording uploaded", `{"createdDate":"20000101","deviceId":"C1","mediaObjectCount":131,"ownerId":"O","presignedContentUrl":"https://h/O/C1/recordings/946684800123.mp4?s=x","presignedLastImageUrl":"https://h/O/C1/lastImage.jpg?s=x","presignedThumbnailUrl":"https://h/O/C1/recordings/946684800123_thumb.jpg?s=x","recordingStopped":true,"resource":"mediaUploadNotification","uniqueId":"O_C1"}`,
			[]string{"recording C1 946684800123 video/mp4 https://h/O/C1/recordings/946684800123.mp4?s=x https://h/O/C1/recordings/946684800123_thumb.jpg?s=x"}},
		{"recording with an unexpected name", `{"deviceId":"C1","presignedContentUrl":"https://h/clip","resource":"mediaUploadNotification"}`,
			[]string{"recording C1 -62135596800000  https://h/clip "}},
		// privacyActive as pyaarlo's camera.py reads it; not yet seen live.
		{"camera turned off", `{"action":"is","from":"B","properties":{"privacyActive":true},"resource":"cameras/C1"}`,
			[]string{"state C1 connected=- battery=- on=false"}},
		{"camera on in get devices", `{"action":"is","resource":"devices","from":"B","devices":{"C1":{"properties":{"privacyActive":"False"}}}}`,
			[]string{"state C1 connected=- battery=- on=true"}},
		{"snapshot started", `{"action":"is","from":"B","properties":{"activityState":"fullFrameSnapshot"},"resource":"cameras/C1"}`, nil},
	}
	for _, tc := range cases {
		es, err := testStream(t).received([]byte(tc.raw))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := describeAll(es); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}

	st := testStream(t)
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
		s := fmt.Sprintf("state %s connected=%s battery=%s", e.ID, conn, batt)
		if e.On != nil {
			s += fmt.Sprintf(" on=%v", *e.On)
		}
		return s
	case SnapshotReady:
		return fmt.Sprintf("snapshot %s %s", e.ID, e.URL)
	case RecordingAdded:
		return fmt.Sprintf("recording %s %d %s %s %s", e.CameraID, e.Created.UnixMilli(), e.ContentType, e.URL, e.ThumbnailURL)
	}
	return fmt.Sprintf("%#v", e)
}
