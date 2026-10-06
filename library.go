package arlo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
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

// RecordingAdded reports a new recording in the library once its upload
// ends, its URLs readable at once. Arlo's notice holds less than Library:
// Duration, Reason and Object are unknown, and Created and ContentType come
// from the URL's object name. Arlo was seen to announce each recording
// once; CameraID and Created identify it should one repeat. Recordings made
// while Run is disconnected are not announced: Library has them.
type RecordingAdded struct{ Recording }

func (RecordingAdded) isEvent() {}

// recordingAdded reads a mediaUploadNotification's URLs. The object is named
// after its utcCreatedDate: …/recordings/<epoch ms>.mp4.
func recordingAdded(cameraID, contentURL, thumbnailURL string) RecordingAdded {
	r := Recording{CameraID: cameraID, URL: contentURL, ThumbnailURL: thumbnailURL}
	if u, err := url.Parse(contentURL); err == nil {
		name := path.Base(u.Path)
		ext := path.Ext(name)
		// Named as Library names them; mime's table may lack .mp4.
		r.ContentType = map[string]string{".mp4": "video/mp4", ".jpg": "image/jpg"}[ext]
		if ms, err := strconv.ParseInt(strings.TrimSuffix(name, ext), 10, 64); err == nil {
			r.Created = time.UnixMilli(ms)
		}
	}
	return RecordingAdded{r}
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
// It waits for Run's connection.
func (c *Client) Library(ctx context.Context, from, to time.Time) ([]Recording, error) {
	var rs []Recording
	err := c.h.do(ctx, "library", func(ctx context.Context, _ *stream, _ func(Event)) error {
		var err error
		rs, err = c.api.library(ctx, from, to)
		return err
	})
	return rs, err
}
