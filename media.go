package arlo

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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
// battery. It needs Run to be connected.
func (c *Client) Snapshot(ctx context.Context, cameraID string) error {
	err := c.do(ctx, func(ctx context.Context, st *stream, _ func(Event)) error {
		base, err := st.baseOf(cameraID)
		if err != nil {
			return err
		}
		_, err = c.api.relay(ctx, "/hmsweb/users/devices/fullFrameSnapshot", base, map[string]any{
			"action":          "set",
			"resource":        "cameras/" + cameraID,
			"publishResponse": true,
			"properties":      map[string]any{"activityState": "fullFrameSnapshot"},
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("arlo: snapshot: %w", err)
	}
	return nil
}

// Stream starts a camera's live stream and returns its RTSPS URL. Arlo
// stops the stream when nothing reads it for about 30 seconds. It wakes the
// camera, so it costs battery. It needs Run to be connected.
func (c *Client) Stream(ctx context.Context, cameraID string) (string, error) {
	var u string
	err := c.do(ctx, func(ctx context.Context, st *stream, _ func(Event)) error {
		base, err := st.baseOf(cameraID)
		if err != nil {
			return err
		}
		data, err := c.api.relay(ctx, "/hmsweb/users/devices/startStream", base, map[string]any{
			"action":          "set",
			"resource":        "cameras/" + cameraID,
			"publishResponse": true,
			"responseUrl":     "",
			"properties":      map[string]any{"activityState": "startUserStream", "cameraId": cameraID},
		})
		if err != nil {
			return err
		}
		u, err = streamURL(data)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("arlo: stream: %w", err)
	}
	return u, nil
}

// streamURL reads startStream's answer. Arlo names the RTSPS stream
// rtsp://; pyaarlo fixes it the same way.
func streamURL(data []byte) (string, error) {
	var r struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &r); err != nil || !strings.HasPrefix(r.URL, "rtsp") {
		return "", fmt.Errorf("no stream URL in %s", snippet(data))
	}
	return strings.Replace(r.URL, "rtsp://", "rtsps://", 1), nil
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

// LastImages reads a camera's latest pictures from the device list. It
// needs Run to be connected.
func (c *Client) LastImages(ctx context.Context, cameraID string) (LastImages, error) {
	var li LastImages
	err := c.do(ctx, func(ctx context.Context, _ *stream, _ func(Event)) error {
		devs, err := c.api.devices(ctx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(devs, func(d device) bool { return d.ID == cameraID && d.Type == "camera" })
		if i < 0 {
			return fmt.Errorf("no camera %s", cameraID)
		}
		li = LastImages{Image: devs[i].LastImageURL, Snapshot: devs[i].SnapshotURL}
		return nil
	})
	if err != nil {
		return LastImages{}, fmt.Errorf("arlo: last images: %w", err)
	}
	return li, nil
}
