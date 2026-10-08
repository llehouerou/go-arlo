package arlo

import (
	"cmp"
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
	"time"
	"uuid"

	"github.com/imroc/req/v3"
)

// api talks to Arlo's two hosts as one account: it holds the Session, opens
// it (login.go) and adds the headers, envelopes and redacted dumps every
// call needs. Once Run has started, only its goroutine touches it.
type api struct {
	email       string
	password    string
	code        CodeFunc
	sessionPath string
	dumpDir     string
	log         *slog.Logger
	http        *req.Client
	jar         http.CookieJar
	authHost    string
	apiHost     string
	dumps       int
	sess        session
}

// newAPI returns an api for the hosts at these base URLs. It does not touch
// the network.
func newAPI(cfg Config, authHost, apiHost string) *api {
	jar, _ := cookiejar.New(nil) // never fails with nil options
	return &api{
		email:       cfg.Email,
		password:    cfg.Password,
		code:        cfg.Code,
		sessionPath: cfg.SessionPath,
		dumpDir:     cfg.DumpDir,
		log:         cmp.Or(cfg.Log, slog.Default()),
		jar:         jar,
		http: req.C().ImpersonateChrome().
			SetCookieJar(jar).
			SetUserAgent(userAgent).
			SetCommonHeader("Accept-Language", "en-GB,en;q=0.9,en-US;q=0.8").
			SetTimeout(60 * time.Second),
		authHost: authHost,
		apiHost:  apiHost,
	}
}

// apiError is a well-formed refusal from Arlo, as opposed to a transport
// failure.
type apiError struct {
	path    string
	status  int
	code    int
	arloErr int // the auth host's meta.error, finer than code
	message string
}

func (e *apiError) Error() string {
	switch {
	case e.arloErr != 0:
		return fmt.Sprintf("%s: HTTP %d, code %d, error %d: %s", e.path, e.status, e.code, e.arloErr, e.message)
	case e.code != 0:
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
			Error   int    `json:"error"`
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
		return nil, &apiError{path: path, status: status, code: env.Meta.Code, arloErr: env.Meta.Error, message: env.Meta.Message}
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

func (a *api) authHeaders(authorized bool) map[string]string {
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
		"X-User-Device-Id":              a.sess.DeviceID,
		"X-User-Device-Type":            "BROWSER",
	}
	if authorized {
		h["Authorization"] = base64.StdEncoding.EncodeToString([]byte(a.sess.Token))
	}
	return h
}

// preflight mimics the CORS preflight a browser sends before some
// auth calls. Its outcome does not matter.
func (a *api) preflight(ctx context.Context, path string) {
	_, _ = a.http.R().SetContext(ctx).SetHeaders(a.authHeaders(false)).
		SetHeader("Access-Control-Request-Method", "POST").
		Send(http.MethodOptions, a.authHost+path)
}

// authCall sends one request to the auth host. body is JSON-encoded when
// not nil.
func (a *api) authCall(ctx context.Context, method, path string, authorized bool, body any) (json.RawMessage, error) {
	r := a.http.R().SetContext(ctx).SetHeaders(a.authHeaders(authorized))
	if body != nil {
		r.SetBodyJsonMarshal(body)
	}
	resp, err := r.Send(method, a.authHost+path)
	if err != nil {
		return nil, err
	}
	a.dumpResponse(path, resp)
	return unwrap(path, resp.StatusCode, resp.Bytes())
}

// apiCall sends a request to the API host with the session token. extra
// headers are added to the usual ones; body is JSON-encoded when not nil.
func (a *api) apiCall(ctx context.Context, method, path string, extra map[string]string, body any) (json.RawMessage, error) {
	tid := "FE!" + uuid.NewV4().String()
	u, err := url.Parse(a.apiHost + path)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("eventId", tid)
	q.Set("time", strconv.FormatInt(time.Now().UnixMilli(), 10))
	u.RawQuery = q.Encode()
	r := a.http.R().SetContext(ctx).SetHeaders(map[string]string{
		"Accept":           "application/json",
		"Auth-Version":     "2",
		"Authorization":    a.sess.Token,
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
	a.dumpResponse(path, resp)
	return unwrap(path, resp.StatusCode, resp.Bytes())
}

// Keys whose values are secrets or personal data, lowercased.
var redacted = map[string]bool{
	"token": true, "browserauthcode": true,
	"factorauthcode": true, "userid": true, "email": true, "otp": true,
	"password": true, "factornickname": true, "displayname": true,
	"factordata": true, "firstname": true, "lastname": true,
	"streamurl": true, "url": true, // RTSP URLs with an ingress token
	// Presigned media URLs: anyone holding one can fetch the media.
	"presignedcontenturl": true, "presignedthumbnailurl": true,
	"presignedlastimageurl": true, "presignedfullframesnapshoturl": true,
}

func (a *api) dumpResponse(path string, resp *req.Response) {
	var cookies []string
	for _, ck := range resp.Cookies() {
		cookies = append(cookies, ck.Name)
	}
	a.dump(path, map[string]any{"path": path, "status": resp.StatusCode, "setCookies": cookies}, resp.Bytes())
}

// dump writes body, redacted, with its context fields to DumpDir. Failures
// are logged, never fatal.
func (a *api) dump(name string, fields map[string]any, body []byte) {
	if a.dumpDir == "" {
		return
	}
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		fields["body"] = redact(v)
	} else {
		fields["body"] = snippet(body)
	}
	out, _ := json.MarshalIndent(fields, "", "  ")
	a.dumps++
	file := fmt.Sprintf("%s-%03d%s.json", time.Now().Format("20060102-150405"), a.dumps,
		strings.NewReplacer("/", "_", "?", "_", "+", "_", "#", "_").Replace(name))
	if err := os.MkdirAll(a.dumpDir, 0o700); err == nil {
		err = os.WriteFile(filepath.Join(a.dumpDir, file), out, 0o600)
		if err == nil {
			return
		}
	}
	a.log.Warn("arlo: dump failed", "name", name)
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
