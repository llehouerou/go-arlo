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

// Login opens a Session once and saves it, then returns: to set it up ahead
// of Run, for instance through the first email two-factor. Never run it
// while a Client's Run uses the same SessionPath: each authentication
// rotates the trust cookie, and the loser's copy costs a new two-factor.
func Login(ctx context.Context, cfg Config) error {
	return New(cfg).login(ctx)
}

// login opens a session. It reuses the saved token while Arlo accepts it and
// it is not about to expire; otherwise it authenticates once, with
// two-factor by email until Arlo trusts this client as a browser. Arlo rate
// limits auth attempts with a long cooldown, so login never retries.
func (c *Client) login(ctx context.Context) error {
	a := c.api
	if err := a.load(); err != nil {
		return fmt.Errorf("arlo: read session: %w", err)
	}
	if a.sess.DeviceID == "" {
		a.sess.DeviceID = uuid.NewV4().String()
	}

	if a.sess.Token != "" && time.Until(a.expires()) > renewBefore {
		err := a.validate(ctx)
		var refused *apiError
		switch {
		case err == nil:
			c.log.Info("arlo: saved token still valid", "expires", a.expires())
			return a.startSession(ctx)
		case !errors.As(err, &refused):
			return fmt.Errorf("arlo: validate saved token: %w", err)
		}
		c.log.Info("arlo: saved token refused, authenticating", "err", err)
	}

	if err := c.authenticate(ctx); err != nil {
		return fmt.Errorf("arlo: %w", err)
	}
	return a.startSession(ctx)
}

func (a *api) expires() time.Time { return time.Unix(a.sess.Expires, 0) }

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

func (a *api) setAuth(data json.RawMessage) (authData, error) {
	var d authData
	if err := json.Unmarshal(data, &d); err != nil {
		return d, err
	}
	t := d
	if d.AccessToken != nil {
		t = *d.AccessToken
	}
	if t.Token == "" || t.UserID == "" {
		return d, errors.New("no token in auth answer")
	}
	a.sess.Token, a.sess.UserID, a.sess.Expires = t.Token, t.UserID, t.ExpiresIn
	if bac := cmp.Or(t.BrowserAuthCode, d.BrowserAuthCode); bac != "" {
		a.sess.BrowserAuthCode = bac
	}
	return d, nil
}

// authenticate follows pyaarlo's ArloBackEnd._auth, _validate and
// _pair_auth_code.
func (c *Client) authenticate(ctx context.Context) error {
	a := c.api
	a.preflight(ctx, "/api/auth")
	data, err := a.authCall(ctx, http.MethodPost, "/api/auth", false, map[string]any{
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
	auth, err := a.setAuth(data)
	if err != nil {
		return fmt.Errorf("/api/auth: %w", err)
	}

	paired := false
	if !auth.AuthCompleted {
		if paired, err = c.secondFactor(ctx); err != nil {
			return err
		}
	}

	// Save at once: startAuth on a trusted browser rotates the
	// browser_trust cookie and voids the old one, so losing the new one
	// costs an email 2FA. The token alone also spares the next auth.
	if err := a.save(); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	if err := a.validate(ctx); err != nil {
		return err
	}
	if !paired || a.sess.BrowserAuthCode == "" {
		return nil
	}
	if _, err := a.authCall(ctx, http.MethodPost, "/api/startPairingFactor", true, map[string]any{
		"factorAuthCode": a.sess.BrowserAuthCode,
		"factorData":     "",
		"factorType":     "BROWSER",
	}); err != nil {
		return fmt.Errorf("pair browser: %w", err)
	}
	c.log.Info("arlo: browser paired, next logins skip 2FA")
	if err := a.save(); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return nil
}

// secondFactor completes an auth Arlo left incomplete. A trusted browser
// needs no code; otherwise the code is emailed and the browser gets paired
// afterwards, which it reports.
func (c *Client) secondFactor(ctx context.Context) (paired bool, err error) {
	a := c.api
	a.preflight(ctx, "/api/getFactorId")
	data, err := a.authCall(ctx, http.MethodPost, "/api/getFactorId", true, map[string]any{
		"factorType": "BROWSER",
		"factorData": "",
		"userId":     a.sess.UserID,
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
		a.preflight(ctx, "/api/startAuth")
		data, err := a.authCall(ctx, http.MethodPost, "/api/startAuth", true, map[string]any{
			"factorId":   f.FactorID,
			"factorType": "BROWSER",
			"userId":     a.sess.UserID,
		})
		if err != nil {
			return false, err
		}
		_, err = a.setAuth(data)
		return false, err
	}

	if c.cfg.Code == nil {
		return false, errNeedsCode
	}
	data, err = a.authCall(ctx, http.MethodGet,
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
	a.preflight(ctx, "/api/startAuth")
	// pyaarlo sends factorType BROWSER here too, with the email factor's id.
	data, err = a.authCall(ctx, http.MethodPost, "/api/startAuth", true, map[string]any{
		"factorId":   factorID,
		"factorType": "BROWSER",
		"userId":     a.sess.UserID,
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
	data, err = a.authCall(ctx, http.MethodPost, "/api/finishAuth", true, map[string]any{
		"factorAuthCode":   start.FactorAuthCode,
		"otp":              code,
		"isBrowserTrusted": true,
	})
	if err != nil {
		return false, err
	}
	_, err = a.setAuth(data)
	return true, err
}

func (a *api) validate(ctx context.Context) error {
	_, err := a.authCall(ctx, http.MethodGet,
		"/api/validateAccessToken?data="+strconv.FormatInt(time.Now().Unix(), 10), true, nil)
	return err
}

// startSession fetches the session details the event stream needs.
func (a *api) startSession(ctx context.Context) error {
	data, err := a.apiCall(ctx, http.MethodGet, "/hmsweb/users/session/v3", nil, nil)
	if err != nil {
		return fmt.Errorf("arlo: start session: %w", err)
	}
	var s struct {
		MQTTURL string `json:"mqttUrl"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("arlo: start session: %w", err)
	}
	a.mqttURL = s.MQTTURL
	a.log.Info("arlo: session started", "mqttUrl", s.MQTTURL)
	return nil
}
