package serve_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
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
