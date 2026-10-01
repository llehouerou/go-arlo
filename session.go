package arlo

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// session is what survives restarts. DeviceID and Cookies make Arlo treat us
// as a trusted browser (no 2FA); Token spares a new auth while it is valid.
type session struct {
	DeviceID        string   `json:"deviceId"`
	UserID          string   `json:"userId,omitempty"`
	Token           string   `json:"token,omitempty"`
	Expires         int64    `json:"expires,omitempty"` // Unix time
	BrowserAuthCode string   `json:"browserAuthCode,omitempty"`
	Cookies         []cookie `json:"cookies,omitempty"`
}

type cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// load reads the session file and restores its cookies. A missing file is an
// empty session.
func (a *api) load() error {
	b, err := os.ReadFile(a.sessionPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &a.sess); err != nil {
		return err
	}
	u, err := url.Parse(a.authHost + "/")
	if err != nil {
		return err
	}
	var cs []*http.Cookie
	for _, ck := range a.sess.Cookies {
		cs = append(cs, &http.Cookie{Name: ck.Name, Value: ck.Value, Path: "/"})
	}
	a.jar.SetCookies(u, cs)
	return nil
}

// save writes the session file atomically, readable by its owner only.
func (a *api) save() error {
	u, err := url.Parse(a.authHost + "/")
	if err != nil {
		return err
	}
	a.sess.Cookies = nil
	for _, ck := range a.jar.Cookies(u) {
		a.sess.Cookies = append(a.sess.Cookies, cookie{ck.Name, ck.Value})
	}
	b, err := json.MarshalIndent(a.sess, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(a.sessionPath), ".arlo-session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op once renamed
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), a.sessionPath)
}
