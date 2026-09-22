package serve_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dmitrymomot/go-router/serve"
)

func ExampleRun() {
	// A real program takes this context from signal.NotifyContext, so that
	// Ctrl-C drains the open requests instead of cutting them off.
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		//nolint:errcheck // The example has no better place to report it.
		fmt.Fprint(w, "ok")
	})

	err := serve.Run(ctx, h, serve.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: 5 * time.Second,
		Logger:          slog.New(slog.DiscardHandler),
		// Port 0 settles on a port here, and nowhere earlier.
		OnListen: func(addr net.Addr) { go fetchOnce(addr, stop) },
	})
	// Run gives back nil after a clean drain, so a non-nil error is real.
	fmt.Println("Run returned:", err)
	// Output:
	// 200 ok
	// Run returned: <nil>
}

func fetchOnce(addr net.Addr, stop func()) {
	defer stop()

	res, err := http.Get("http://" + addr.String())
	if err != nil {
		fmt.Println(err)
		return
	}
	defer res.Body.Close() //nolint:errcheck // Nothing left to report it to.

	body, _ := io.ReadAll(res.Body)
	fmt.Println(res.StatusCode, string(body))
}

func ExampleConfig_drainDelay() {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	var ready atomic.Bool
	ready.Store(true)
	draining := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "draining", http.StatusServiceUnavailable)
			return
		}
		//nolint:errcheck // The example has no better place to report it.
		fmt.Fprint(w, "ready")
	})

	err := serve.Run(ctx, mux, serve.Config{
		Addr: "127.0.0.1:0",
		// A real program waits a few seconds, longer than the period at which
		// the load balancer polls /readyz.
		DrainDelay: 500 * time.Millisecond,
		Logger:     slog.New(slog.DiscardHandler),
		OnDrain: func() {
			ready.Store(false)
			close(draining)
		},
		OnListen: func(addr net.Addr) {
			go func() {
				fmt.Println(get("http://" + addr.String() + "/readyz"))
				stop()
				<-draining
				fmt.Println(get("http://" + addr.String() + "/readyz"))
			}()
		},
	})
	fmt.Println("Run returned:", err)
	// Output:
	// 200 ready
	// 503 draining
	// Run returned: <nil>
}

// get fetches url and gives back its status and body on one line.
func get(url string) string {
	res, err := http.Get(url)
	if err != nil {
		return err.Error()
	}
	defer res.Body.Close() //nolint:errcheck // Nothing left to report it to.

	body, _ := io.ReadAll(res.Body)
	return fmt.Sprint(res.StatusCode, " ", strings.TrimSpace(string(body)))
}
