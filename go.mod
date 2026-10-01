module github.com/llehouerou/go-arlo

go 1.27.1

require (
	github.com/eclipse/paho.golang v0.23.0
	github.com/emersion/go-imap/v2 v2.0.0-beta.8
	github.com/emersion/go-message v0.18.2
	github.com/imroc/req/v3 v3.61.0
)

require (
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/icholy/digest v1.2.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.61.0 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

// Published before the history was rewritten to drop personal data from the
// notes; their tags are gone. Use v0.2.0 or later.
retract [v0.1.0, v0.1.1]
