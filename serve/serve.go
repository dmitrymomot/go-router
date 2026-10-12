// Package serve runs HTTP servers that stop when their context does.
//
// [Run] serves a handler until its context ends, then drains the requests in
// flight. [RunAll] does the same for several servers, such as a public one
// beside a private one for probes. The defaults are the safe ones: a header
// deadline, an idle deadline and a bounded drain.
//
//	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
//	defer stop()
//
//	r := router.New(func(http.ResponseWriter, *http.Request) *App { return new(App) })
//	r.GET("/", home)
//
//	err := serve.Run(ctx, r, serve.Config{Addr: ":8080", DrainDelay: 5 * time.Second})
//
// A long-lived handler, such as a server-sent event stream, sees its request
// context end when the drain starts. See [ErrDraining].
package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"time"
)

// DefaultShutdownTimeout is how long [Run] waits for the requests in flight
// when Config.ShutdownTimeout is zero.
const DefaultShutdownTimeout = 10 * time.Second

// DefaultReadHeaderTimeout is the header deadline that [Run] applies when
// Config.ReadHeaderTimeout is zero. It keeps a slow client from holding a
// connection open forever.
const DefaultReadHeaderTimeout = 10 * time.Second

// DefaultIdleTimeout is how long [Run] keeps an idle keep-alive connection
// when Config.IdleTimeout is zero.
const DefaultIdleTimeout = 120 * time.Second

// ErrDraining is the cause of the request contexts that [Run] cancels when the
// drain starts, right after DrainDelay. A long-lived handler tells a drain
// from a client that went away with [context.Cause]:
//
//	if errors.Is(context.Cause(c), serve.ErrDraining) { ... }
var ErrDraining = errors.New("serve: the server is draining")

// Config is what [Run] builds the server from. Set Addr, or hand over a
// Listener of your own; a Listener wins, and Run closes it either way.
//
// Every timeout of zero takes the default of net/http, except
// ReadHeaderTimeout, which takes [DefaultReadHeaderTimeout], IdleTimeout,
// which takes [DefaultIdleTimeout], and ShutdownTimeout, which takes
// [DefaultShutdownTimeout]. A negative ReadHeaderTimeout or IdleTimeout means
// no deadline, and a negative ShutdownTimeout closes the connections at once
// rather than draining them.
//
// ReadTimeout has no default, because the right bound depends on the size of
// the uploads the server takes; set it when no route accepts a large body.
// WriteTimeout stays zero on purpose, so a server-sent event stream is not cut
// off; bound a plain handler with a timeout middleware instead.
//
// Once the drain starts, Run cancels the context of every request in flight
// with [ErrDraining] as the cause, so a long-lived handler such as a
// server-sent event stream ends on its own and the drain does not run out of
// time. OnServer may set BaseContext; Run wraps it and adds that cancellation.
//
// OnListen runs with the address the server actually listens on, which names
// the port that ":0" chose. OnServer sees the [http.Server] before it serves,
// for a setting this Config does not carry; an error from it stops Run.
//
// Once ctx ends, Run calls OnDrain and keeps answering for DrainDelay, so that
// a load balancer polling a readiness probe takes the server out of rotation
// before the drain starts. It then drains within ShutdownTimeout. OnDrain runs
// on its own goroutine while the handlers still answer: the flag it flips has
// to be atomic, and an OnDrain that blocks pushes back both the delay and the
// drain. A server whose serving fails before ctx ends closes at once, with no
// OnDrain and no delay. A negative DrainDelay is an error.
//
// Any certificate in Certificates turns the server into an HTTPS one, over TLS
// 1.3 and h2. With a TLSConfig of your own, the certificates join the ones it
// carries. Load one with [tls.LoadX509KeyPair] or [tls.X509KeyPair].
//
// Keep-alives stay on during the delay, because closing idle connections while
// the balancer still sends would fail its requests. To turn them off anyway,
// keep the server from OnServer and call SetKeepAlivesEnabled(false) on it
// from OnDrain.
type Config struct {
	Addr              string
	Network           string
	Listener          net.Listener
	TLSConfig         *tls.Config
	Certificates      []tls.Certificate
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	DrainDelay        time.Duration
	ShutdownTimeout   time.Duration
	Logger            *slog.Logger
	OnListen          func(net.Addr)
	OnDrain           func()
	OnServer          func(*http.Server) error
}

// Run serves h until ctx ends, waits Config.DrainDelay, then drains the
// requests in flight and reports once the server has stopped. A cancelled ctx
// is the ordinary way to stop, so Run reports nil for it.
//
// Run reports an error for a nil context, a nil handler, a Config that names
// neither an address nor a listener, a negative DrainDelay, a TLS config with
// no certificate, a listener it cannot open, and a drain that runs out of
// time. It checks the certificate after OnServer and before it listens, even
// for a ctx that has already ended.
func Run(ctx context.Context, h http.Handler, cfg Config) error {
	// Run closes a caller-supplied listener on the serving path, so it owns it
	// from here on and has to close it on every path, early returns included.
	if cfg.Listener != nil {
		defer cfg.Listener.Close() //nolint:errcheck // Reported by whoever opened it.
	}
	if ctx == nil {
		return errors.New("serve: Run needs a context")
	}
	in, err := prepare(h, cfg)
	if err != nil {
		return err
	}
	if err := in.build(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := in.open(ctx); err != nil {
		return err
	}
	defer in.closeOwn()
	if cfg.OnListen != nil {
		cfg.OnListen(in.ln.Addr())
	}
	return in.serve(ctx)
}

// instance is one server on its way from a Config to serving. [Run] and
// [RunAll] take each server through the same steps: prepare, build, open and
// serve.
type instance struct {
	h   http.Handler
	srv *http.Server

	// draining ends the contexts of the requests in flight, with ErrDraining.
	draining       context.Context
	cancelDraining context.CancelCauseFunc

	ln  net.Listener
	cfg Config
}

func prepare(h http.Handler, cfg Config) (*instance, error) {
	if h == nil {
		return nil, errors.New("serve: the server needs a handler")
	}
	if cfg.Listener == nil && cfg.Addr == "" {
		return nil, errors.New("serve: the server needs Config.Addr or Config.Listener")
	}
	if cfg.DrainDelay < 0 {
		return nil, errors.New("serve: Config.DrainDelay is negative")
	}
	return &instance{h: h, cfg: cfg}, nil
}

func (in *instance) build() error {
	logger := in.cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	in.srv = &http.Server{
		Addr:              in.cfg.Addr,
		Handler:           in.h,
		TLSConfig:         tlsConfig(in.cfg),
		ReadHeaderTimeout: headerTimeout(in.cfg.ReadHeaderTimeout),
		ReadTimeout:       in.cfg.ReadTimeout,
		WriteTimeout:      in.cfg.WriteTimeout,
		IdleTimeout:       idleTimeout(in.cfg.IdleTimeout),

		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	if in.cfg.OnServer != nil {
		if err := in.cfg.OnServer(in.srv); err != nil {
			return err
		}
	}
	if in.srv.TLSConfig != nil && !hasCertificate(in.srv.TLSConfig) {
		return errors.New("serve: the TLS config has no certificate; add one through Config.Certificates, Config.TLSConfig or OnServer")
	}
	return nil
}

// hasCertificate follows the rule of [http.Server.ServeTLS], which Run calls
// with no certificate files: the config has to carry a certificate of its own.
// Keep it in step with configHasCert in net/http.
func hasCertificate(c *tls.Config) bool {
	return len(c.Certificates) > 0 || c.GetCertificate != nil || c.GetConfigForClient != nil
}

func (in *instance) open(ctx context.Context) error {
	ln, err := listen(ctx, in.cfg)
	if err != nil {
		return err
	}
	in.ln = ln
	return nil
}

// closeOwn closes the listener that open opened. A listener of the Config is
// left to whoever owns it.
func (in *instance) closeOwn() {
	if in.ln != nil && in.cfg.Listener == nil {
		_ = in.ln.Close() // The listener is going away either way.
	}
}

// withDrainContext wraps BaseContext of the server, the one OnServer may have
// set, so that the context of every request ends when the drain starts.
func (in *instance) withDrainContext(ctx context.Context) {
	in.draining, in.cancelDraining = context.WithCancelCause(context.Background())
	user := in.srv.BaseContext
	in.srv.BaseContext = func(l net.Listener) context.Context {
		base := context.WithoutCancel(ctx)
		if user != nil {
			if b := user(l); b != nil {
				base = b
			}
		}
		bctx, cancel := context.WithCancelCause(base)
		// The stop function is dropped: the callback runs once, and Serve calls
		// BaseContext once.
		context.AfterFunc(in.draining, func() { cancel(ErrDraining) })
		return bctx
	}
}

func (in *instance) serve(ctx context.Context) error {
	in.withDrainContext(ctx)
	stopped := make(chan struct{})
	drained := make(chan struct{})
	var drainErr error

	go func() {
		defer close(drained)
		select {
		case <-ctx.Done():
		case <-stopped:
			return
		}
		// Both cases may be ready at once, and select picks one at random. A
		// server whose serving failed is closed already and must not drain.
		select {
		case <-stopped:
			return
		default:
		}
		drainErr = in.drain(ctx, stopped)
	}()

	err := serveOn(in.srv, in.ln)
	var closeErr error
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		if closeErr = in.srv.Close(); closeErr != nil {
			closeErr = fmt.Errorf("serve: close connections after serving failed: %w", closeErr)
		}
	}
	close(stopped)
	<-drained

	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return joinErrors(err, closeErr, drainErr)
}

// joinErrors is errors.Join, except that one error is returned as itself.
// errors.Join always wraps, which would break the identity comparisons callers
// make against the error Run gives back.
func joinErrors(errs ...error) error {
	nonNil := errs[:0]
	for _, err := range errs {
		if err != nil {
			nonNil = append(nonNil, err)
		}
	}
	if len(nonNil) == 1 {
		return nonNil[0]
	}
	return errors.Join(nonNil...)
}

func listen(ctx context.Context, cfg Config) (net.Listener, error) {
	if cfg.Listener != nil {
		return cfg.Listener, nil
	}
	network := cfg.Network
	if network == "" {
		network = "tcp"
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, network, cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("serve: listen on %s %s: %w", network, cfg.Addr, err)
	}
	return ln, nil
}

func serveOn(srv *http.Server, ln net.Listener) error {
	if srv.TLSConfig != nil {
		return srv.ServeTLS(ln, "", "")
	}
	return srv.Serve(ln)
}

// drain calls OnDrain, waits DrainDelay while the server still answers, then
// shuts it down. A server whose serving fails during the delay is already
// closed, so there is nothing left to drain.
func (in *instance) drain(ctx context.Context, stopped <-chan struct{}) error {
	if in.cfg.OnDrain != nil {
		in.cfg.OnDrain()
	}
	if d := in.cfg.DrainDelay; d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-stopped:
			return nil
		}
	}
	in.cancelDraining(ErrDraining)
	return shutdown(ctx, in.srv, in.cfg.ShutdownTimeout)
}

func shutdown(ctx context.Context, srv *http.Server, timeout time.Duration) error {
	if timeout < 0 {
		if err := srv.Close(); err != nil {
			return fmt.Errorf("serve: close the connections: %w", err)
		}
		return nil
	}
	if timeout == 0 {
		timeout = DefaultShutdownTimeout
	}

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	switch err := srv.Shutdown(sctx); {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		_ = srv.Close() // The drain already failed; the close reports nothing new.
		return fmt.Errorf("serve: the drain did not finish in %s: %w", timeout, err)
	default:
		return fmt.Errorf("serve: shut down: %w", err)
	}
}

func idleTimeout(d time.Duration) time.Duration {
	switch {
	case d == 0:
		return DefaultIdleTimeout
	case d < 0:
		return 0
	default:
		return d
	}
}

func headerTimeout(d time.Duration) time.Duration {
	switch {
	case d == 0:
		return DefaultReadHeaderTimeout
	case d < 0:
		return 0
	default:
		return d
	}
}

func tlsConfig(cfg Config) *tls.Config {
	certs := cfg.Certificates
	if cfg.TLSConfig == nil {
		if len(certs) == 0 {
			return nil
		}
		return &tls.Config{
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h2", "http/1.1"},
			Certificates: certs,
		}
	}

	c := cfg.TLSConfig.Clone()
	if len(certs) > 0 {
		c.Certificates = slices.Concat(c.Certificates, certs)
	}
	return c
}
