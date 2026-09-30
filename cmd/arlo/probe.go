package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

// Same agent pyaarlo sends by default ("arlo" in its USER_AGENTS).
const userAgent = "(iPhone15,2 18_1_1) iOS Arlo 5.4.3"

// probe sends credential-free requests to Arlo's auth and API hosts, once with
// a Chrome TLS fingerprint and once with plain net/http, to tell a Cloudflare
// block from an Arlo answer. It never calls an auth endpoint with a body, so it
// does not count against the auth rate limit.
func probe() error {
	type target struct{ method, url string }
	targets := []target{
		{http.MethodOptions, "https://ocapi-app.arlo.com/api/auth"},
		{http.MethodGet, "https://myapi.arlo.com/hmsweb/users/session/v3"},
	}
	chrome := req.C().ImpersonateChrome().SetUserAgent(userAgent).SetTimeout(20 * time.Second)
	plain := &http.Client{Timeout: 20 * time.Second}

	for _, name := range []string{"chrome", "net/http"} {
		for _, t := range targets {
			headers := map[string]string{
				"Origin":  "https://my.arlo.com",
				"Referer": "https://my.arlo.com/",
			}
			if t.method == http.MethodOptions {
				headers["Access-Control-Request-Method"] = "POST"
			}
			fmt.Printf("%-8s %-7s %s\n", name, t.method, t.url)

			var resp *http.Response
			var err error
			if name == "chrome" {
				var r *req.Response
				r, err = chrome.R().SetHeaders(headers).DisableAutoReadResponse().Send(t.method, t.url)
				if err == nil {
					resp = r.Response
				}
			} else {
				var r *http.Request
				if r, err = http.NewRequest(t.method, t.url, nil); err != nil {
					return err
				}
				for k, v := range headers {
					r.Header.Set(k, v)
				}
				r.Header.Set("User-Agent", userAgent)
				resp, err = plain.Do(r)
			}
			if err != nil {
				fmt.Printf("         error: %v\n", err)
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			resp.Body.Close()
			fmt.Printf("         %s %s  server=%q cf-mitigated=%q type=%q\n",
				resp.Proto, resp.Status, resp.Header.Get("Server"),
				resp.Header.Get("Cf-Mitigated"), resp.Header.Get("Content-Type"))
			fmt.Printf("         verdict: %s\n", verdict(resp, string(body)))
			if len(body) > 0 {
				fmt.Printf("         body: %s\n", strings.Join(strings.Fields(string(body)), " "))
			}
		}
	}
	return nil
}

func verdict(resp *http.Response, body string) string {
	if resp.Header.Get("Cf-Mitigated") != "" || strings.Contains(body, "Just a moment") ||
		strings.Contains(body, "cf-chl") || strings.Contains(body, "Attention Required") {
		return "BLOCKED by Cloudflare"
	}
	return "reached Arlo"
}
