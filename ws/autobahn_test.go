//go:build autobahn

package ws

import (
	"net/http"
	"os"
	"testing"
	"time"
)

// TestAutobahnServer serves an echo endpoint at :9001 for the fuzzing client
// of the Autobahn test suite, until WS_AUTOBAHN_FOR passes (default 10m). Run
// it with
//
//	go test -tags autobahn -run Autobahn ./ws &
//	docker run --rm --network host -v "$PWD/ws/testdata/autobahn:/config" \
//		crossbario/autobahn-testsuite wstest -m fuzzingclient -s /config/fuzzingclient.json
func TestAutobahnServer(t *testing.T) {
	d, err := time.ParseDuration(os.Getenv("WS_AUTOBAHN_FOR"))
	if err != nil {
		d = 10 * time.Minute
	}
	r := newRouter()
	r.GET("/", echo(nil, ReadLimit(32<<20)))
	srv := &http.Server{Addr: ":9001", Handler: r, ReadHeaderTimeout: 10 * time.Second}
	go func() { time.Sleep(d); _ = srv.Close() }()
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
