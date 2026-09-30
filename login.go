package arlo

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"
)

// renewBefore is how long before its expiry a token is replaced. Tokens last
// two hours.
const renewBefore = 10 * time.Minute

var (
	// errAuthRefused marks Arlo refusing /api/auth: bad credentials or the
	// rate limit. Retrying soon only prolongs the cooldown.
	errAuthRefused = errors.New("auth refused")
	// errNeedsCode marks a 2FA that cannot be done without Config.Code.
	errNeedsCode = errors.New("2FA needed but no code source configured")
)

// Login opens a session. It reuses the saved token while Arlo accepts it and
// it is not about to expire; otherwise it authenticates once, with
// two-factor by email until Arlo trusts this client as a browser. Arlo rate
// limits auth attempts with a long cooldown, so Login never retries.
func (c *Client) Login(ctx context.Context) error {
	if err := c.load(); err != nil {
		return fmt.Errorf("arlo: read session: %w", err)
	}
	if c.sess.DeviceID == "" {
		c.sess.DeviceID = uuid.NewV4().String()
	}

	if c.sess.Token != "" && time.Until(c.expires()) > renewBefore {
		err := c.validate(ctx)
		var refused *apiError
		switch {
		case err == nil:
			c.log.Info("arlo: saved token still valid", "expires", c.expires())
			return c.startSession(ctx)
		case !errors.As(err, &refused):
			return fmt.Errorf("arlo: validate saved token: %w", err)
		}
		c.log.Info("arlo: saved token refused, authenticating", "err", err)
	}

	if err := c.authenticate(ctx); err != nil {
		return fmt.Errorf("arlo: %w", err)
	}
	return c.startSession(ctx)
}

func (c *Client) expires() time.Time { return time.Unix(c.sess.Expires, 0) }

// authData is the token part of /api/auth, startAuth and finishAuth answers.
// The last two nest it under accessToken.
type authData struct {
	Token           string    `json:"token"`
	UserID          string    `json:"userId"`
	ExpiresIn       int64     `json:"expiresIn"` // a Unix time, despite the name
	AuthCompleted   bool      `json:"authCompleted"`
	BrowserAuthCode string    `json:"browserAuthCode"`
	AccessToken     *authData `json:"accessToken"`
}

func (c *Client) setAuth(data json.RawMessage) (authData, error) {
	var a authData
	if err := json.Unmarshal(data, &a); err != nil {
		return a, err
	}
	t := a
	if a.AccessToken != nil {
		t = *a.AccessToken
	}
	if t.Token == "" || t.UserID == "" {
		return a, errors.New("no token in auth answer")
	}
	c.sess.Token, c.sess.UserID, c.sess.Expires = t.Token, t.UserID, t.ExpiresIn
	if bac := cmp.Or(t.BrowserAuthCode, a.BrowserAuthCode); bac != "" {
		c.sess.BrowserAuthCode = bac
	}
	return a, nil
}

// authenticate follows pyaarlo's ArloBackEnd._auth, _validate and
// _pair_auth_code.
func (c *Client) authenticate(ctx context.Context) error {
	c.preflight(ctx, "/api/auth")
	data, err := c.authCall(ctx, http.MethodPost, "/api/auth", false, map[string]any{
		"email":     c.cfg.Email,
		"password":  base64.StdEncoding.EncodeToString([]byte(c.cfg.Password)),
		"language":  "en",
		"EnvSource": "prod",
	})
	var refused *apiError
	if errors.As(err, &refused) {
		return fmt.Errorf("%w: %w", errAuthRefused, err)
	}
	if err != nil {
		return err
	}
	a, err := c.setAuth(data)
	if err != nil {
		return fmt.Errorf("/api/auth: %w", err)
	}

	paired := false
	if !a.AuthCompleted {
		if paired, err = c.secondFactor(ctx); err != nil {
			return err
		}
	}

	if err := c.validate(ctx); err != nil {
		return err
	}
	// Save before pairing: the token alone spares the next auth.
	if err := c.save(); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	if !paired || c.sess.BrowserAuthCode == "" {
		return nil
	}
	if _, err := c.authCall(ctx, http.MethodPost, "/api/startPairingFactor", true, map[string]any{
		"factorAuthCode": c.sess.BrowserAuthCode,
		"factorData":     "",
		"factorType":     "BROWSER",
	}); err != nil {
		return fmt.Errorf("pair browser: %w", err)
	}
	c.log.Info("arlo: browser paired, next logins skip 2FA")
	if err := c.save(); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return nil
}

// secondFactor completes an auth Arlo left incomplete. A trusted browser
// needs no code; otherwise the code is emailed and the browser gets paired
// afterwards, which it reports.
func (c *Client) secondFactor(ctx context.Context) (paired bool, err error) {
	c.preflight(ctx, "/api/getFactorId")
	data, err := c.authCall(ctx, http.MethodPost, "/api/getFactorId", true, map[string]any{
		"factorType": "BROWSER",
		"factorData": "",
		"userId":     c.sess.UserID,
	})
	var refused *apiError
	if err != nil && !errors.As(err, &refused) {
		return false, err
	}
	if err == nil {
		var f struct {
			FactorID string `json:"factorId"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			return false, fmt.Errorf("/api/getFactorId: %w", err)
		}
		c.log.Info("arlo: trusted browser, no 2FA")
		c.preflight(ctx, "/api/startAuth")
		data, err := c.authCall(ctx, http.MethodPost, "/api/startAuth", true, map[string]any{
			"factorId":   f.FactorID,
			"factorType": "BROWSER",
			"userId":     c.sess.UserID,
		})
		if err != nil {
			return false, err
		}
		_, err = c.setAuth(data)
		return false, err
	}

	if c.cfg.Code == nil {
		return false, errNeedsCode
	}
	data, err = c.authCall(ctx, http.MethodGet,
		"/api/getFactors?data="+strconv.FormatInt(time.Now().Unix(), 10), true, nil)
	if err != nil {
		return false, err
	}
	var factors struct {
		Items []struct {
			FactorID   string `json:"factorId"`
			FactorType string `json:"factorType"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &factors); err != nil {
		return false, fmt.Errorf("/api/getFactors: %w", err)
	}
	factorID := ""
	for _, f := range factors.Items {
		if strings.EqualFold(f.FactorType, "EMAIL") {
			factorID = f.FactorID
			break
		}
	}
	if factorID == "" {
		return false, errors.New("2FA needed but the account has no email factor")
	}

	c.log.Info("arlo: 2FA by email")
	since := time.Now()
	c.preflight(ctx, "/api/startAuth")
	// pyaarlo sends factorType BROWSER here too, with the email factor's id.
	data, err = c.authCall(ctx, http.MethodPost, "/api/startAuth", true, map[string]any{
		"factorId":   factorID,
		"factorType": "BROWSER",
		"userId":     c.sess.UserID,
	})
	if err != nil {
		return false, err
	}
	var start struct {
		FactorAuthCode string `json:"factorAuthCode"`
	}
	if err := json.Unmarshal(data, &start); err != nil || start.FactorAuthCode == "" {
		return false, errors.New("/api/startAuth: no factorAuthCode")
	}
	code, err := c.cfg.Code(ctx, since)
	if err != nil {
		return false, fmt.Errorf("2FA code: %w", err)
	}
	data, err = c.authCall(ctx, http.MethodPost, "/api/finishAuth", true, map[string]any{
		"factorAuthCode":   start.FactorAuthCode,
		"otp":              code,
		"isBrowserTrusted": true,
	})
	if err != nil {
		return false, err
	}
	_, err = c.setAuth(data)
	return true, err
}

func (c *Client) validate(ctx context.Context) error {
	_, err := c.authCall(ctx, http.MethodGet,
		"/api/validateAccessToken?data="+strconv.FormatInt(time.Now().Unix(), 10), true, nil)
	return err
}

// startSession fetches the session details the event stream needs.
func (c *Client) startSession(ctx context.Context) error {
	data, err := c.apiCall(ctx, http.MethodGet, "/hmsweb/users/session/v3", nil, nil)
	if err != nil {
		return fmt.Errorf("arlo: start session: %w", err)
	}
	var s struct {
		MQTTURL       string `json:"mqttUrl"`
		MultiLocation bool   `json:"supportsMultiLocation"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("arlo: start session: %w", err)
	}
	c.mqttURL, c.multiLocation = s.MQTTURL, s.MultiLocation
	c.log.Info("arlo: session started", "mqttUrl", s.MQTTURL, "multiLocation", s.MultiLocation)
	return nil
}
