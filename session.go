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
	BrowserAuthCode string   `json:"browserAuthCode,omitempty"`
	Cookies         []cookie `json:"cookies,omitempty"`
}

type cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// load reads the session file and restores its cookies. A missing file is an
// empty session.
func (c *Client) load() error {
	b, err := os.ReadFile(c.cfg.SessionPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &c.sess); err != nil {
		return err
	}
	u, err := url.Parse(c.authHost + "/")
	if err != nil {
		return err
	}
	var cs []*http.Cookie
	for _, ck := range c.sess.Cookies {
		cs = append(cs, &http.Cookie{Name: ck.Name, Value: ck.Value, Path: "/"})
	}
	c.jar.SetCookies(u, cs)
	return nil
}

// save writes the session file atomically, readable by its owner only.
func (c *Client) save() error {
	u, err := url.Parse(c.authHost + "/")
	if err != nil {
		return err
	}
	c.sess.Cookies = nil
	for _, ck := range c.jar.Cookies(u) {
		c.sess.Cookies = append(c.sess.Cookies, cookie{ck.Name, ck.Value})
	}
	b, err := json.MarshalIndent(c.sess, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.cfg.SessionPath), ".arlo-session-*")
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
	return os.Rename(f.Name(), c.cfg.SessionPath)
}
