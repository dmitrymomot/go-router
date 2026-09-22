package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Server is one server for [RunAll]: the handler, the Config and the options
// that [Run] takes as separate arguments.
type Server struct {
	Handler http.Handler
	Config  Config
	Options []Option
}

// RunAll runs every server until ctx ends, or until any one of them stops, and
// reports once all of them have stopped. It suits a public server beside a
// private one for probes and metrics.
//
// RunAll checks every server and opens every listener before any of them
// serves, so a mistake in one server starts none. One server stopping stops
// the rest the way a cancelled ctx does: each of them calls OnDrain and waits
// its DrainDelay before it drains.
//
// The servers stop in the order given. Each one starts its drain only after
// every server before it has returned, so a private server listed last keeps
// answering, its 503 from /readyz included, through the whole public drain.
// The longest stop is the sum of DrainDelay and ShutdownTimeout over all the
// servers, and the grace period of the orchestrator has to cover it. Setting
// DrainDelay on the first server alone is usually enough.
//
// Each error names the index of its server, on the first line of its text;
// [errors.Is] and [errors.As] see through that. RunAll closes every
// Config.Listener on every path, as Run does, and reports nil for a cancelled
// ctx.
func RunAll(ctx context.Context, servers ...Server) error {
	for _, s := range servers {
		if s.Config.Listener != nil {
			defer s.Config.Listener.Close() //nolint:errcheck // Reported by whoever opened it.
		}
	}
	if ctx == nil {
		return errors.New("serve: RunAll needs a context")
	}
	if len(servers) == 0 {
		return errors.New("serve: RunAll needs a server")
	}

	ins := make([]*instance, len(servers))
	for i, s := range servers {
		in, err := prepare(s.Handler, s.Config, s.Options)
		if err != nil {
			return serverError{i: i, err: err}
		}
		ins[i] = in
	}
	if ctx.Err() != nil {
		return nil
	}
	for i, in := range ins {
		if err := in.build(); err != nil {
			return serverError{i: i, err: err}
		}
	}
	defer func() {
		for _, in := range ins {
			in.closeOwn()
		}
	}()
	for i, in := range ins {
		if err := in.open(ctx); err != nil {
			return serverError{i: i, err: err}
		}
	}
	for _, in := range ins {
		if in.cfg.OnListen != nil {
			in.cfg.OnListen(in.ln.Addr())
		}
	}

	// The coordinator alone decides when a server starts to drain. Every
	// server runs on a context detached from ctx, so its values still arrive,
	// and the coordinator cancels server i only once servers 0 to i-1 have
	// all returned, whichever of them stopped first.
	stop, stopAll := context.WithCancel(ctx)
	defer stopAll()

	cancels := make([]context.CancelFunc, len(ins))
	done := make([]chan struct{}, len(ins))
	errs := make([]error, len(ins))
	var wg sync.WaitGroup
	for i, in := range ins {
		sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		cancels[i] = cancel
		done[i] = make(chan struct{})
		wg.Go(func() {
			defer close(done[i])
			defer stopAll()
			if err := in.serve(sctx); err != nil {
				errs[i] = serverError{i: i, err: err}
			}
		})
	}
	wg.Go(func() {
		<-stop.Done()
		for i := range ins {
			cancels[i]()
			<-done[i]
		}
	})
	wg.Wait()

	return joinErrors(errs...)
}

// serverError names the server of [RunAll] that err came from.
type serverError struct {
	err error
	i   int
}

func (e serverError) Error() string {
	return fmt.Sprintf("serve: server %d: %s", e.i, strings.TrimPrefix(e.err.Error(), "serve: "))
}

func (e serverError) Unwrap() error { return e.err }
