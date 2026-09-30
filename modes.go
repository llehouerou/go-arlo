package arlo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Mode is a location's alarm mode. An active custom mode is reported as
// Arlo names it, "custom".
type Mode string

const (
	Standby Mode = "standby"
	ArmHome Mode = "armHome"
	ArmAway Mode = "armAway"
)

// ModeChanged reports a location's mode, on connection and on every change.
type ModeChanged struct {
	LocationID   string
	LocationName string
	Mode         Mode
}

func (ModeChanged) isEvent() {}

type location struct {
	ID       string   `json:"locationId"`
	Name     string   `json:"locationName"`
	Gateways []string `json:"gatewayDeviceIds"`
}

// modeHeaders are the extra headers of pyaarlo's location calls.
func (c *Client) modeHeaders() map[string]string {
	return map[string]string{"x-forwarded-user": c.sess.UserID, "x-user-device-id": c.sess.UserID}
}

// location returns the location holding the account's base stations. An
// account can also see locations of its own with no device, and shared
// locations name their gateways "<ownerId>_<deviceId>".
//
// ponytail: one location with bases only (ours); SetMode would need a
// location argument for accounts with several.
func (c *Client) location(ctx context.Context, bases []device) (location, error) {
	data, err := c.apiCall(ctx, http.MethodGet, "/hmsdevicemanagement/users/"+c.sess.UserID+"/locations", nil, nil)
	if err != nil {
		return location{}, err
	}
	var ls struct {
		User   []location `json:"userLocations"`
		Shared []location `json:"sharedLocations"`
	}
	if err := json.Unmarshal(data, &ls); err != nil {
		return location{}, fmt.Errorf("locations: %w", err)
	}
	var found []location
	for _, l := range append(ls.User, ls.Shared...) {
		if slices.ContainsFunc(l.Gateways, func(g string) bool {
			return slices.ContainsFunc(bases, func(b device) bool { return g == b.ID || strings.HasSuffix(g, "_"+b.ID) })
		}) {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		return location{}, fmt.Errorf("locations: %d hold a base station, only one is supported", len(found))
	}
	return found[0], nil
}

// activeMode reads a location's mode and the revision a change must quote.
func (c *Client) activeMode(ctx context.Context, loc location) (Mode, int64, error) {
	data, err := c.apiCall(ctx, http.MethodGet,
		"/hmsweb/automation/v3/activeMode?locationId="+url.QueryEscape(loc.ID), c.modeHeaders(), nil)
	if err != nil {
		return "", 0, err
	}
	var m struct {
		Properties struct {
			Mode Mode `json:"mode"`
		} `json:"properties"`
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", 0, fmt.Errorf("active mode: %w", err)
	}
	return m.Properties.Mode, m.Revision, nil
}

// setMode changes a location's mode, quoting the current revision as
// pyaarlo does.
func (c *Client) setMode(ctx context.Context, loc location, mode Mode) error {
	_, rev, err := c.activeMode(ctx, loc)
	if err != nil {
		return err
	}
	_, err = c.apiCall(ctx, http.MethodPut,
		"/hmsweb/automation/v3/activeMode?locationId="+url.QueryEscape(loc.ID)+"&revision="+strconv.FormatInt(rev, 10),
		c.modeHeaders(), map[string]any{"mode": mode})
	return err
}

var errNotConnected = errors.New("arlo: not connected")

// command is work Run does on behalf of another goroutine, so that it uses
// the session only Run's goroutine touches.
type command struct {
	fn   func(ctx context.Context, emit func(Event)) error
	done chan error
}

// do hands fn to Run and waits for its result. It fails at once when Run is
// not connected.
func (c *Client) do(ctx context.Context, fn func(context.Context, func(Event)) error) error {
	if !c.connected.Load() {
		return errNotConnected
	}
	cmd := command{fn: fn, done: make(chan error, 1)}
	select {
	case c.cmds <- cmd:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-cmd.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetMode sets the mode of the account's location. It needs Run to be
// connected, and reports the new mode as a ModeChanged event.
func (c *Client) SetMode(ctx context.Context, mode Mode) error {
	return c.do(ctx, func(ctx context.Context, emit func(Event)) error {
		if err := c.resolveLocation(ctx); err != nil {
			return fmt.Errorf("arlo: set mode %s: %w", mode, err)
		}
		if err := c.setMode(ctx, c.loc, mode); err != nil {
			return fmt.Errorf("arlo: set mode %s: %w", mode, err)
		}
		emit(ModeChanged{LocationID: c.loc.ID, LocationName: c.loc.Name, Mode: mode})
		return nil
	})
}

// resolveLocation finds the location once per session; a failure is
// retried on the next call.
func (c *Client) resolveLocation(ctx context.Context) error {
	if c.loc.ID != "" {
		return nil
	}
	loc, err := c.location(ctx, c.bases)
	c.loc = loc
	return err
}

// readMode reports the location's current mode.
func (c *Client) readMode(ctx context.Context, emit func(Event)) error {
	if err := c.resolveLocation(ctx); err != nil {
		return fmt.Errorf("arlo: read mode: %w", err)
	}
	mode, _, err := c.activeMode(ctx, c.loc)
	if err != nil {
		return fmt.Errorf("arlo: read mode: %w", err)
	}
	emit(ModeChanged{LocationID: c.loc.ID, LocationName: c.loc.Name, Mode: mode})
	return nil
}
