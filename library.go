package arlo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Recording is an entry of the account's media library: a video, or a
// snapshot. Its URLs are presigned: a plain GET fetches them, for a while.
type Recording struct {
	CameraID     string
	Created      time.Time
	Duration     time.Duration
	ContentType  string // "video/mp4" or "image/jpg"
	Reason       string // what triggered it, as Arlo names it
	Object       string // what the camera saw: "person", "vehicle", …; often empty
	URL          string
	ThumbnailURL string
}

type libraryEntry struct {
	DeviceID     string `json:"deviceId"`
	Created      int64  `json:"utcCreatedDate"` // epoch ms
	Duration     int    `json:"mediaDurationSecond"`
	ContentType  string `json:"contentType"`
	Reason       string `json:"reason"`
	Object       string `json:"objCategory"`
	URL          string `json:"presignedContentUrl"`
	ThumbnailURL string `json:"presignedThumbnailUrl"`
}

// library lists the recordings of the days from to to, in their location.
func (a *api) library(ctx context.Context, from, to time.Time) ([]Recording, error) {
	data, err := a.apiCall(ctx, http.MethodPost, "/hmsweb/users/library", nil,
		map[string]string{"dateFrom": from.Format("20060102"), "dateTo": to.Format("20060102")})
	if err != nil {
		return nil, err
	}
	var es []libraryEntry
	if err := json.Unmarshal(data, &es); err != nil {
		return nil, fmt.Errorf("library: %w", err)
	}
	rs := make([]Recording, len(es))
	for i, e := range es {
		rs[i] = Recording{
			CameraID:     e.DeviceID,
			Created:      time.UnixMilli(e.Created),
			Duration:     time.Duration(e.Duration) * time.Second,
			ContentType:  e.ContentType,
			Reason:       e.Reason,
			Object:       e.Object,
			URL:          e.URL,
			ThumbnailURL: e.ThumbnailURL,
		}
	}
	return rs, nil
}

// Library lists the recordings of the days from to to, as Arlo orders them.
// It needs Run to be connected.
func (c *Client) Library(ctx context.Context, from, to time.Time) ([]Recording, error) {
	var rs []Recording
	err := c.do(ctx, "library", func(ctx context.Context, _ *stream, _ func(Event)) error {
		var err error
		rs, err = c.api.library(ctx, from, to)
		return err
	})
	return rs, err
}
