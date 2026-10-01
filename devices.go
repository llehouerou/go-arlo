package arlo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"uuid"
)

// device is an entry of /hmsweb/v2/users/devices, reduced to what we use.
type device struct {
	ID       string   `json:"deviceId"`
	Type     string   `json:"deviceType"`
	Name     string   `json:"deviceName"`
	Model    string   `json:"modelId"`
	ParentID string   `json:"parentId"`
	XCloudID string   `json:"xCloudId"`
	Topics   []string `json:"allowedMqttTopics"`
}

func (a *api) devices(ctx context.Context) ([]device, error) {
	data, err := a.apiCall(ctx, http.MethodGet,
		"/hmsweb/v2/users/devices?t="+strconv.FormatInt(time.Now().UnixMilli(), 10), nil, nil)
	if err != nil {
		return nil, err
	}
	var ds []device
	if err := json.Unmarshal(data, &ds); err != nil {
		return nil, fmt.Errorf("devices: %w", err)
	}
	return ds, nil
}

// webID is how Arlo addresses this client in notifications.
func (a *api) webID() string { return a.sess.UserID + "_web" }

// notify posts a message for a base station; Arlo relays it and the answer
// comes back on the event stream.
func (a *api) notify(ctx context.Context, base device, body map[string]any) error {
	body["to"] = base.ID
	body["from"] = a.webID()
	body["transId"] = "web!" + uuid.NewV4().String()
	_, err := a.apiCall(ctx, http.MethodPost, "/hmsweb/users/devices/notify/"+base.ID,
		map[string]string{"xcloudId": base.XCloudID}, body)
	return err
}

// ping subscribes this client to a base station's events, as pyaarlo does
// every minute.
func (a *api) ping(ctx context.Context, base device) error {
	return a.notify(ctx, base, map[string]any{
		"action":          "set",
		"resource":        "subscriptions/" + a.webID(),
		"publishResponse": false,
		"properties":      map[string]any{"devices": []string{base.ID}},
	})
}
