package arlo

import "testing"

func TestStreamURL(t *testing.T) {
	// Real answer shape, URL trimmed.
	u, err := streamURL([]byte(`{"bandwidthTestUrl":"rtmps://h:80/bandwidth/","nextgenServer":false,"url":"rtsp://1.2.3.4:443/vzmodulelive/C1_1?egressToken=x"}`))
	if err != nil || u != "rtsps://1.2.3.4:443/vzmodulelive/C1_1?egressToken=x" {
		t.Errorf("got %q, %v", u, err)
	}
	if _, err := streamURL([]byte(`{}`)); err == nil {
		t.Error("no error without a URL")
	}
}
