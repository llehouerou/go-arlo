package arlo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeArlo plays Arlo for one account: both hosts and the Event stream.
// Base station B, with its siren pseudo device, pairs cameras C1 and C2; the
// account's Location L1 is shared by its owner. The browser is trusted once
// the pairing cookie it sets comes back; only the last token it issued is
// accepted. Like Arlo, it answers some requests on the Event stream.
type fakeArlo struct {
	t     *testing.T
	mu    sync.Mutex
	calls []string
	// issued is the last token handed out; "" validates nothing.
	issued   string
	tokens   int
	refuse   bool // /api/auth answers 401
	mfaDown  bool // the trust check fails as in Arlo's identity outages
	deviceID string
	// trust is the only browser_trust value accepted; Arlo rotates it on
	// every trusted startAuth.
	trust      int
	pingStatus int // HTTP status of base pings; 0 is 200
	mode       Mode
	revision   int64
	stream     *fakeStream // the open Event stream connection, if any
	topics     []string    // subscribed by the last dial
}

type fakeStream struct {
	msgs chan streamMessage
	done chan struct{}
}

func newFakeArlo(t *testing.T) *fakeArlo {
	return &fakeArlo{t: t, mode: Standby, revision: 41}
}

const fakeMQTTURL = "ssl://mqtt.example:8883"

var fakeTopics = []string{"d/XB/out/cameras/#", "d/XB/out/devices/#"}

func (f *fakeArlo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	f.calls = append(f.calls, r.URL.Path)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		f.serveAPI(w, r, body)
		return
	}

	if id := r.Header.Get("X-User-Device-Id"); f.deviceID == "" {
		f.deviceID = id
	} else if id != f.deviceID {
		f.t.Errorf("%s: device id %q, want %q", r.URL.Path, id, f.deviceID)
	}
	authorized := r.Header.Get("Authorization") == base64.StdEncoding.EncodeToString([]byte(f.issued)) && f.issued != ""
	trusted := false
	if ck, err := r.Cookie("trust"); err == nil && f.trust > 0 && ck.Value == fmt.Sprint(f.trust) {
		trusted = true
	}
	meta := func(data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"code": 200}, "data": data})
	}
	refuse := func(code int) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"code": code, "error": 9204, "message": "no"}})
	}
	expires := time.Now().Add(2 * time.Hour).Unix()

	switch r.URL.Path {
	case "/api/auth":
		if f.refuse {
			refuse(http.StatusUnauthorized)
			return
		}
		if pw, _ := base64.StdEncoding.DecodeString(body["password"].(string)); string(pw) != "secret" {
			f.t.Errorf("password %q", pw)
		}
		meta(map[string]any{"token": f.token(), "userId": "U1", "expiresIn": expires, "authCompleted": false})
	case "/api/getFactorId":
		if f.mfaDown {
			_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"code": 400, "message": "Mfa disabled by service"}})
			return
		}
		if !authorized || !trusted {
			// What Arlo really answers for an untrusted browser.
			_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"code": 400, "error": 9261, "message": "Invalid factor data"}})
			return
		}
		meta(map[string]any{"factorId": "F-browser"})
	case "/api/getFactors":
		meta(map[string]any{"items": []any{
			map[string]any{"factorId": "F-sms", "factorType": "SMS"},
			map[string]any{"factorId": "F-mail", "factorType": "EMAIL"},
		}})
	case "/api/startAuth":
		switch body["factorId"] {
		case "F-browser":
			f.trust++
			http.SetCookie(w, &http.Cookie{Name: "trust", Value: fmt.Sprint(f.trust), Path: "/"})
			meta(map[string]any{"accessToken": map[string]any{"token": f.token(), "userId": "U1", "expiresIn": expires}})
		case "F-mail":
			meta(map[string]any{"factorAuthCode": "FAC"})
		default:
			f.t.Errorf("startAuth factor %v", body["factorId"])
		}
	case "/api/finishAuth":
		if body["otp"] != "123456" || body["factorAuthCode"] != "FAC" || body["isBrowserTrusted"] != true {
			f.t.Errorf("finishAuth body %v", body)
		}
		meta(map[string]any{"accessToken": map[string]any{"token": f.token(), "userId": "U1", "expiresIn": expires, "browserAuthCode": "BAC"}})
	case "/api/validateAccessToken":
		if !authorized {
			refuse(http.StatusUnauthorized)
			return
		}
		meta(map[string]any{})
	case "/api/startPairingFactor":
		if body["factorAuthCode"] != "BAC" {
			f.t.Errorf("pairing body %v", body)
		}
		f.trust++
		http.SetCookie(w, &http.Cookie{Name: "trust", Value: fmt.Sprint(f.trust), Path: "/"})
		meta(map[string]any{})
	default:
		f.t.Errorf("unexpected %s", r.URL.Path)
	}
}

func (f *fakeArlo) token() string {
	f.tokens++
	f.issued = fmt.Sprintf("tok-%d", f.tokens)
	return f.issued
}

// serveAPI plays the API host.
func (f *fakeArlo) serveAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	ok := func(data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
	}
	refuse := func(code int) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "data": map[string]any{"reason": "no"}})
	}
	if r.URL.Query().Get("eventId") == "" {
		f.t.Errorf("%s: no eventId", r.URL.Path)
	}
	if f.issued == "" || r.Header.Get("Authorization") != f.issued {
		refuse(http.StatusUnauthorized)
		return
	}
	// relayed checks a message for base station B.
	relayed := func(resource string) {
		if body["to"] != "B" || body["from"] != "U1_web" || r.Header.Get("xcloudId") != "XB" || body["resource"] != resource {
			f.t.Errorf("%s: relay %v, xcloudId %q", r.URL.Path, body, r.Header.Get("xcloudId"))
		}
	}

	switch r.URL.Path {
	case "/hmsweb/users/session/v3":
		ok(map[string]any{"mqttUrl": fakeMQTTURL})
	case "/hmsweb/v2/users/devices":
		dev := func(id, typ, name string) map[string]any {
			return map[string]any{"deviceId": id, "deviceType": typ, "deviceName": name, "parentId": "B",
				"xCloudId": "XB", "allowedMqttTopics": fakeTopics,
				"presignedLastImageUrl": "https://last/" + id, "presignedFullFrameSnapshotUrl": "https://snap/" + id}
		}
		ok([]any{dev("B", "basestation", "Base"), dev("B", "siren", ""), dev("C1", "camera", "Gate"), dev("C2", "camera", "Porch")})
	case "/hmsweb/users/devices/notify/B":
		switch body["resource"] {
		case "subscriptions/U1_web": // ping
			relayed("subscriptions/U1_web")
			if f.pingStatus != 0 {
				refuse(f.pingStatus)
				return
			}
		case "devices":
			relayed("devices")
			f.push(`{"action":"is","resource":"devices","from":"B","devices":{
				"C2":{"properties":{"batteryLevel":45,"connectionState":"available","motionDetected":false}},
				"C1":{"properties":{"batteryLevel":80,"connectionState":"available","motionDetected":"False"}}}}`)
		case "cameras/C1": // camera on/off
			relayed("cameras/C1")
			privacy, ok := body["properties"].(map[string]any)["privacyActive"].(bool)
			if !ok || body["action"] != "set" || body["publishResponse"] != true {
				f.t.Errorf("camera on/off %v", body)
			}
			f.push(fmt.Sprintf(`{"action":"is","from":"B","properties":{"privacyActive":%v},"resource":"cameras/C1"}`, privacy))
		default:
			f.t.Errorf("notify %v", body)
		}
		ok(map[string]any{})
	case "/hmsdevicemanagement/users/U1/locations":
		ok(map[string]any{
			"userLocations":   []any{map[string]any{"locationId": "own", "locationName": "Mine"}},
			"sharedLocations": []any{map[string]any{"locationId": "L1", "locationName": "Home", "gatewayDeviceIds": []string{"OWNER_B"}}},
		})
	case "/hmsweb/automation/v3/activeMode":
		if r.URL.Query().Get("locationId") != "L1" || r.Header.Get("x-forwarded-user") != "U1" {
			f.t.Errorf("activeMode %s, headers %v", r.URL, r.Header)
		}
		if r.Method == http.MethodPut {
			if rev := r.URL.Query().Get("revision"); rev != fmt.Sprint(f.revision) {
				f.t.Errorf("revision %s, want %d", rev, f.revision)
			}
			f.mode = Mode(body["mode"].(string))
			f.revision++
			f.push(fmt.Sprintf(`{"action":"is","from":"B","resource":"devices/B/states","states":{"activeMode":%q}}`, f.mode))
		}
		ok(map[string]any{"properties": map[string]any{"mode": f.mode}, "revision": f.revision})
	case "/hmsweb/users/devices/fullFrameSnapshot":
		relayed("cameras/C1")
		f.push(`{"action":"fullFrameSnapshotAvailable","from":"B","properties":{"presignedFullFrameSnapshotUrl":"https://snap/C1-new"},"resource":"cameras/C1"}`)
		ok(map[string]any{})
	case "/hmsweb/users/devices/startStream":
		relayed("cameras/C1")
		ok(map[string]any{"bandwidthTestUrl": "rtmps://h:80/bandwidth/", "url": "rtsp://1.2.3.4:443/vzmodulelive/C1_1?egressToken=x"})
	case "/hmsweb/users/library":
		if body["dateFrom"] != "20000101" || body["dateTo"] != "20000103" {
			f.t.Errorf("library %v", body)
		}
		ok([]any{map[string]any{"deviceId": "C1", "utcCreatedDate": 946684800123, "mediaDurationSecond": 12,
			"contentType": "video/mp4", "reason": "motionRecord", "objCategory": "Person",
			"presignedContentUrl": "https://v", "presignedThumbnailUrl": "https://t", "extra": 1}})
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}
}

// dial is the Event stream adapter: a connection fed by what fakeArlo pushes.
func (f *fakeArlo) dial(_ context.Context, url, userID, token string, topics []string) (eventConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if url != fakeMQTTURL || userID != "U1" || token != f.issued {
		return eventConn{}, errors.New("fake broker: refused")
	}
	s := &fakeStream{msgs: make(chan streamMessage, 64), done: make(chan struct{})}
	f.stream, f.topics = s, topics
	return eventConn{msgs: s.msgs, done: s.done, close: func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.stream == s {
			f.stream = nil
		}
	}}, nil
}

// push sends a packet on the open Event stream; f.mu must be held.
func (f *fakeArlo) push(packet string) {
	if f.stream != nil {
		f.stream.msgs <- streamMessage{topic: "d/XB/out/test", payload: []byte(packet)}
	}
}

func (f *fakeArlo) send(packet string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.push(packet)
}

// drop cuts the open Event stream connection.
func (f *fakeArlo) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stream != nil {
		close(f.stream.done)
		f.stream = nil
	}
}

// set changes the fake's state under its lock.
func (f *fakeArlo) set(fn func(*fakeArlo)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeArlo) takeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

// pipeListener serves HTTP over net.Pipe: unlike loopback sockets, a
// goroutine blocked on a pipe lets a synctest bubble go idle.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "pipe", Net: "pipe"} }

func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	srv, cli := net.Pipe()
	select {
	case l.conns <- srv:
		return cli, nil
	case <-l.done:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
