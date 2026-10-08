package arlo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
)

// SnapshotReady reports a camera's new full-frame snapshot, whoever asked
// for it. URL is a presigned JPEG: a plain GET fetches it, for a while.
type SnapshotReady struct {
	ID  string
	URL string
}

func (SnapshotReady) isEvent() {}

// Snapshot asks a camera for a full-frame snapshot, reported as
// SnapshotReady some seconds later. It wakes the camera, so it costs
// battery. It waits for Run's connection.
func (c *Client) Snapshot(ctx context.Context, cameraID string) error {
	return c.h.do(ctx, "snapshot", func(ctx context.Context, st *stream, _ func(Event)) error {
		_, err := c.setCamera(ctx, st, "/hmsweb/users/devices/fullFrameSnapshot", cameraID, map[string]any{
			"properties": map[string]any{"activityState": "fullFrameSnapshot"},
		})
		return err
	})
}

// Stream starts a camera's live stream and returns its RTSPS URL. Called
// while the stream runs, it returns a new URL to the same stream; both can
// be read at once. Arlo sets no time limit and stops the stream when
// nothing reads it. It wakes the camera, so it costs battery. It waits for
// Run's connection.
func (c *Client) Stream(ctx context.Context, cameraID string) (string, error) {
	var u string
	err := c.h.do(ctx, "stream", func(ctx context.Context, st *stream, _ func(Event)) error {
		data, err := c.setCamera(ctx, st, "/hmsweb/users/devices/startStream", cameraID, map[string]any{
			"responseUrl": "",
			"properties":  map[string]any{"activityState": "startUserStream", "cameraId": cameraID},
		})
		if err != nil {
			return err
		}
		u, err = streamURL(data)
		return err
	})
	return u, err
}

// streamURL reads startStream's answer. Its URL says rtsp, yet the server on
// port 443 only speaks RTSP over TLS (docs/NOTES.md, "Live stream").
func streamURL(data []byte) (string, error) {
	var r struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("no stream URL in %s", snippet(data))
	}
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "rtsp" && u.Scheme != "rtsps") {
		return "", fmt.Errorf("no stream URL in %s", snippet(data))
	}
	u.Scheme = "rtsps"
	return u.String(), nil
}

// SetCameraOn turns a camera on or off; off, it neither detects, records
// nor streams. The camera reports the change as a DeviceState. It waits for
// Run's connection.
func (c *Client) SetCameraOn(ctx context.Context, cameraID string, on bool) error {
	name := "camera off"
	if on {
		name = "camera on"
	}
	return c.h.do(ctx, name, func(ctx context.Context, st *stream, _ func(Event)) error {
		_, err := c.setCamera(ctx, st, "", cameraID, map[string]any{
			"properties": map[string]any{"privacyActive": !on},
		})
		return err
	})
}

// setCamera relays a set message for a camera through its base station to
// path, or to the base's notify when path is empty, and returns Arlo's
// immediate answer. body holds the message's own fields, like properties.
func (c *Client) setCamera(ctx context.Context, st *stream, path, cameraID string, body map[string]any) (json.RawMessage, error) {
	base, err := st.baseOf(cameraID)
	if err != nil {
		return nil, err
	}
	if path == "" {
		path = "/hmsweb/users/devices/notify/" + base.ID
	}
	body["action"] = "set"
	body["resource"] = "cameras/" + cameraID
	body["publishResponse"] = true
	return c.api.relay(ctx, path, base, body)
}

// baseOf returns the base station a camera pairs with.
func (s *stream) baseOf(cameraID string) (device, error) {
	i := slices.IndexFunc(s.devices, func(d Device) bool { return d.ID == cameraID && d.Type == "camera" })
	if i < 0 {
		return device{}, fmt.Errorf("no camera %s", cameraID)
	}
	j := slices.IndexFunc(s.bases, func(b device) bool { return b.ID == s.devices[i].BaseID })
	if j < 0 {
		return device{}, fmt.Errorf("no base station for camera %s", cameraID)
	}
	return s.bases[j], nil
}

// LastImages are a camera's latest pictures, read without waking it. Each
// is a presigned JPEG URL, valid 24 hours; the Last-Modified header of its
// GET dates the picture.
type LastImages struct {
	Image    string // 640×357, uploaded after each recording or stream
	Snapshot string // 1920×1072, the latest full-frame snapshot, whoever asked
}

// LastImages returns a camera's latest pictures from memory, without a
// request: Run keeps them from the device list it reads on every
// connection and token renewal, less than two hours apart, and from
// SnapshotReady. It waits for Run's connection.
func (c *Client) LastImages(ctx context.Context, cameraID string) (LastImages, error) {
	var li LastImages
	err := c.h.do(ctx, "last images", func(_ context.Context, st *stream, _ func(Event)) error {
		var ok bool
		if li, ok = st.images[cameraID]; !ok {
			return fmt.Errorf("no camera %s", cameraID)
		}
		return nil
	})
	return li, err
}
