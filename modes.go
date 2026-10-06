package arlo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
func (a *api) modeHeaders() map[string]string {
	return map[string]string{"x-forwarded-user": a.sess.UserID, "x-user-device-id": a.sess.UserID}
}

// locations fetches the account's own and shared locations.
func (a *api) locations(ctx context.Context) (own, shared []location, err error) {
	data, err := a.apiCall(ctx, http.MethodGet, "/hmsdevicemanagement/users/"+a.sess.UserID+"/locations", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var ls struct {
		Own    []location `json:"userLocations"`
		Shared []location `json:"sharedLocations"`
	}
	if err := json.Unmarshal(data, &ls); err != nil {
		return nil, nil, fmt.Errorf("locations: %w", err)
	}
	return ls.Own, ls.Shared, nil
}

// activeMode reads a location's mode and the revision a change must quote.
func (a *api) activeMode(ctx context.Context, loc location) (Mode, int64, error) {
	data, err := a.apiCall(ctx, http.MethodGet,
		"/hmsweb/automation/v3/activeMode?locationId="+url.QueryEscape(loc.ID), a.modeHeaders(), nil)
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
func (a *api) setMode(ctx context.Context, loc location, mode Mode) error {
	_, rev, err := a.activeMode(ctx, loc)
	if err != nil {
		return err
	}
	_, err = a.apiCall(ctx, http.MethodPut,
		"/hmsweb/automation/v3/activeMode?locationId="+url.QueryEscape(loc.ID)+"&revision="+strconv.FormatInt(rev, 10),
		a.modeHeaders(), map[string]any{"mode": mode})
	return err
}

// ErrNotRunning is a command's error when Run has returned and not been
// called again, or returns while the command waits for its connection.
var ErrNotRunning = errors.New("arlo: Run not running")

// command is work Run does on behalf of another goroutine, so that only Run's
// goroutine touches the session and the stream.
type command struct {
	fn   func(ctx context.Context, st *stream, emit func(Event)) error
	done chan error
}

// do hands fn to Run once it is connected and waits for its result, naming
// fn's errors after the command. Called before Run's first call, it waits
// for it. ctx bounds the whole wait: Run may be in a backoff of up to an
// hour.
func (c *Client) do(ctx context.Context, name string, fn func(context.Context, *stream, func(Event)) error) error {
	c.mu.Lock()
	stopped, runDone := c.ran && !c.running, c.runDone
	c.mu.Unlock()
	if stopped {
		return ErrNotRunning
	}
	cmd := command{fn: fn, done: make(chan error, 1)}
	select {
	case c.cmds <- cmd: // only a connected Run receives
	case <-runDone:
		return ErrNotRunning
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-cmd.done:
		if err != nil {
			return fmt.Errorf("arlo: %s: %w", name, err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetMode sets the mode of the account's location, once Run is connected,
// and reports the new mode as a ModeChanged event.
func (c *Client) SetMode(ctx context.Context, mode Mode) error {
	return c.do(ctx, "set mode "+string(mode), func(ctx context.Context, st *stream, emit func(Event)) error {
		if err := c.resolveLocation(ctx, st); err != nil {
			return err
		}
		if err := c.api.setMode(ctx, st.loc, mode); err != nil {
			return err
		}
		emit(st.modeChanged(mode))
		return nil
	})
}

// resolveLocation finds the location once per connection; a failure is
// retried on the next call.
func (c *Client) resolveLocation(ctx context.Context, st *stream) error {
	if st.loc.ID != "" {
		return nil
	}
	own, shared, err := c.api.locations(ctx)
	if err != nil {
		return err
	}
	return st.located(own, shared)
}

// readMode reports the location's current mode.
func (c *Client) readMode(ctx context.Context, st *stream, emit func(Event)) error {
	if err := c.resolveLocation(ctx, st); err != nil {
		return fmt.Errorf("arlo: read mode: %w", err)
	}
	mode, _, err := c.api.activeMode(ctx, st.loc)
	if err != nil {
		return fmt.Errorf("arlo: read mode: %w", err)
	}
	emit(st.modeChanged(mode))
	return nil
}
