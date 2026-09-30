// Package arlo is a client for Arlo's cloud API, ported from pyaarlo 0.8.0.23.
package arlo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/imroc/req/v3"
)

const (
	defaultAuthHost = "https://ocapi-app.arlo.com"
	defaultAPIHost  = "https://myapi.arlo.com"
	origin          = "https://my.arlo.com"
	// pyaarlo's default ("arlo" in its USER_AGENTS).
	userAgent = "(iPhone15,2 18_1_1) iOS Arlo 5.4.3"
)

// CodeFunc returns the 6-digit code Arlo emails for two-factor
// authentication. since is when the code was requested.
type CodeFunc func(ctx context.Context, since time.Time) (string, error)

// Config configures a Client.
type Config struct {
	Email    string
	Password string
	// SessionPath is the file holding the session between runs: it spares
	// the two-factor step, so it must survive restarts.
	SessionPath string
	// Code supplies the two-factor code. Only needed until the client is a
	// trusted browser.
	Code CodeFunc
	// DumpDir, when set, receives every response of the login sequence with
	// secrets redacted, to debug without spending auth attempts.
	DumpDir string
	Log     *slog.Logger
}

// Client talks to Arlo on behalf of one account.
type Client struct {
	cfg      Config
	log      *slog.Logger
	http     *req.Client
	jar      http.CookieJar
	authHost string
	apiHost  string
	dumps    int

	// Once Run has started, only its goroutine touches these; SetMode goes
	// through cmds.
	sess    session
	mqttURL string
	bases   []device
	loc     location

	cmds      chan command
	connected atomic.Bool
}

// New returns a client. It does not touch the network: see Login.
func New(cfg Config) *Client {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	jar, _ := cookiejar.New(nil) // never fails with nil options
	return &Client{
		cfg: cfg,
		log: log,
		jar: jar,
		http: req.C().ImpersonateChrome().
			SetCookieJar(jar).
			SetUserAgent(userAgent).
			SetCommonHeader("Accept-Language", "en-GB,en;q=0.9,en-US;q=0.8").
			SetTimeout(60 * time.Second),
		authHost: defaultAuthHost,
		apiHost:  defaultAPIHost,
		cmds:     make(chan command),
	}
}

// apiError is a well-formed refusal from Arlo, as opposed to a transport
// failure.
type apiError struct {
	path    string
	status  int
	code    int
	message string
}

func (e *apiError) Error() string {
	if e.code != 0 {
		return fmt.Sprintf("%s: HTTP %d, code %d: %s", e.path, e.status, e.code, e.message)
	}
	return fmt.Sprintf("%s: HTTP %d %s", e.path, e.status, e.message)
}

// unwrap extracts the payload of Arlo's two response envelopes:
// {"meta": {"code": 200}, "data": …} from the auth host and
// {"success": true, "data": …} from the API host.
func unwrap(path string, status int, body []byte) (json.RawMessage, error) {
	var env struct {
		Meta *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"meta"`
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, &apiError{path: path, status: status, message: snippet(body)}
	}
	switch {
	case env.Meta != nil && env.Meta.Code == 200 && status == http.StatusOK:
		return env.Data, nil
	case env.Meta != nil:
		return nil, &apiError{path: path, status: status, code: env.Meta.Code, message: env.Meta.Message}
	case env.Success != nil && *env.Success && status == http.StatusOK:
		if env.Data == nil {
			return json.RawMessage("{}"), nil
		}
		return env.Data, nil
	}
	return nil, &apiError{path: path, status: status, message: snippet(body)}
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func (c *Client) authHeaders(authorized bool) map[string]string {
	h := map[string]string{
		"Accept":                        "application/json, text/plain, */*",
		"Cache-Control":                 "no-cache",
		"Content-Type":                  "application/json",
		"Origin":                        origin,
		"Pragma":                        "no-cache",
		"Priority":                      "u=1, i",
		"Referer":                       origin + "/",
		"X-Service-Version":             "3",
		"X-User-Device-Automation-Name": "QlJPV1NFUg==", // base64("BROWSER")
		"X-User-Device-Id":              c.sess.DeviceID,
		"X-User-Device-Type":            "BROWSER",
	}
	if authorized {
		h["Authorization"] = base64.StdEncoding.EncodeToString([]byte(c.sess.Token))
	}
	return h
}

// preflight mimics the browser's CORS preflight pyaarlo sends before some
// auth calls. Its outcome does not matter.
func (c *Client) preflight(ctx context.Context, path string) {
	_, _ = c.http.R().SetContext(ctx).SetHeaders(c.authHeaders(false)).
		SetHeader("Access-Control-Request-Method", "POST").
		Send(http.MethodOptions, c.authHost+path)
}

// authCall sends one request to the auth host. body is JSON-encoded when
// not nil.
func (c *Client) authCall(ctx context.Context, method, path string, authorized bool, body any) (json.RawMessage, error) {
	r := c.http.R().SetContext(ctx).SetHeaders(c.authHeaders(authorized))
	if body != nil {
		r.SetBodyJsonMarshal(body)
	}
	resp, err := r.Send(method, c.authHost+path)
	if err != nil {
		return nil, err
	}
	c.dumpResponse(path, resp)
	return unwrap(path, resp.StatusCode, resp.Bytes())
}

// apiCall sends a request to the API host with the session token. extra
// headers are added to the usual ones; body is JSON-encoded when not nil.
func (c *Client) apiCall(ctx context.Context, method, path string, extra map[string]string, body any) (json.RawMessage, error) {
	tid := "FE!" + uuid.NewV4().String()
	u, err := url.Parse(c.apiHost + path)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("eventId", tid)
	q.Set("time", strconv.FormatInt(time.Now().UnixMilli(), 10))
	u.RawQuery = q.Encode()
	r := c.http.R().SetContext(ctx).SetHeaders(map[string]string{
		"Accept":           "application/json",
		"Auth-Version":     "2",
		"Authorization":    c.sess.Token,
		"Cache-Control":    "no-cache",
		"Content-Type":     "application/json; charset=utf-8;",
		"Origin":           origin,
		"Pragma":           "no-cache",
		"Priority":         "u=1, i",
		"Referer":          origin + "/",
		"SchemaVersion":    "1",
		"x-transaction-id": tid,
	}).SetHeaders(extra)
	if body != nil {
		r.SetBodyJsonMarshal(body)
	}
	resp, err := r.Send(method, u.String())
	if err != nil {
		return nil, err
	}
	c.dumpResponse(path, resp)
	return unwrap(path, resp.StatusCode, resp.Bytes())
}

// Keys whose values are secrets or personal data, lowercased.
var redacted = map[string]bool{
	"token": true, "browserauthcode": true,
	"factorauthcode": true, "userid": true, "email": true, "otp": true,
	"password": true, "factornickname": true, "displayname": true,
	"factordata": true, "firstname": true, "lastname": true,
	"streamurl": true, // RTSP URL with an ingress token
}

func (c *Client) dumpResponse(path string, resp *req.Response) {
	var cookies []string
	for _, ck := range resp.Cookies() {
		cookies = append(cookies, ck.Name)
	}
	c.dump(path, map[string]any{"path": path, "status": resp.StatusCode, "setCookies": cookies}, resp.Bytes())
}

// dump writes body, redacted, with its context fields to DumpDir. Failures
// are logged, never fatal.
func (c *Client) dump(name string, fields map[string]any, body []byte) {
	if c.cfg.DumpDir == "" {
		return
	}
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		fields["body"] = redact(v)
	} else {
		fields["body"] = snippet(body)
	}
	out, _ := json.MarshalIndent(fields, "", "  ")
	c.dumps++
	file := fmt.Sprintf("%s-%03d%s.json", time.Now().Format("20060102-150405"), c.dumps,
		strings.NewReplacer("/", "_", "?", "_", "+", "_", "#", "_").Replace(name))
	if err := os.MkdirAll(c.cfg.DumpDir, 0o700); err == nil {
		err = os.WriteFile(filepath.Join(c.cfg.DumpDir, file), out, 0o600)
		if err == nil {
			return
		}
	}
	c.log.Warn("arlo: dump failed", "name", name)
}

func redact(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if redacted[strings.ToLower(k)] {
				v[k] = "REDACTED"
			} else {
				v[k] = redact(x)
			}
		}
	case []any:
		for i, x := range v {
			v[i] = redact(x)
		}
	}
	return v
}
