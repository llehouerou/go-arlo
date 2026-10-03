package arlo

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLibrary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/hmsweb/users/library" ||
			string(b) != `{"dateFrom":"20261001","dateTo":"20261003"}` {
			t.Errorf("unexpected %s %s %s", r.Method, r.URL.Path, b)
		}
		_, _ = io.WriteString(w, `{"success":true,"data":[{"deviceId":"C1","utcCreatedDate":1790000000123,
			"mediaDurationSecond":12,"contentType":"video/mp4","reason":"motionRecord","objCategory":"Person",
			"presignedContentUrl":"https://v","presignedThumbnailUrl":"https://t","extra":1}]}`)
	}))
	defer srv.Close()

	a := newAPI(Config{}, srv.URL, srv.URL)
	rs, err := a.library(t.Context(), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := Recording{CameraID: "C1", Created: time.UnixMilli(1790000000123), Duration: 12 * time.Second,
		ContentType: "video/mp4", Reason: "motionRecord", Object: "Person", URL: "https://v", ThumbnailURL: "https://t"}
	if len(rs) != 1 || rs[0] != want {
		t.Errorf("got %+v", rs)
	}
}
