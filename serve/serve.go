// Package serve runs an HTTP server that stops when its context does.
package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
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

// Config is what [Run] builds the server from. Set Addr, or hand over a
// Listener of your own; a Listener wins, and Run closes it either way.
//
// Every timeout of zero takes the default of net/http, except
// ReadHeaderTimeout, which takes [DefaultReadHeaderTimeout], and
// ShutdownTimeout, which takes [DefaultShutdownTimeout]. A negative
// ReadHeaderTimeout means no deadline, and a negative ShutdownTimeout closes
// the connections at once rather than draining them.
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
// drain. A server whose serving fails closes at once, with no OnDrain and no
// delay. A negative DrainDelay is an error.
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

// Option adds a TLS certificate to the server. See [CertFiles], [CertPEM] and
// [CertFS].
type Option func(*options) error

type options struct {
	certs []tls.Certificate
}

// CertFiles loads a certificate and its key from disk.
func CertFiles(certPath, keyPath string) Option {
	return func(o *options) error {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return fmt.Errorf("serve: load the certificate %q and the key %q: %w", certPath, keyPath, err)
		}
		o.certs = append(o.certs, cert)
		return nil
	}
}

// CertPEM takes a certificate and its key as PEM bytes, which suits a secret
// that arrives through the environment.
func CertPEM(cert, key []byte) Option {
	return func(o *options) error {
		c, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return fmt.Errorf("serve: read the PEM certificate and key: %w", err)
		}
		o.certs = append(o.certs, c)
		return nil
	}
}

// CertFS loads a certificate and its key from fsys, such as an embed.FS.
func CertFS(fsys fs.FS, certPath, keyPath string) Option {
	return func(o *options) error {
		if fsys == nil {
			return errors.New("serve: CertFS needs a file system")
		}
		cert, err := fs.ReadFile(fsys, certPath)
		if err != nil {
			return fmt.Errorf("serve: read the certificate %q: %w", certPath, err)
		}
		key, err := fs.ReadFile(fsys, keyPath)
		if err != nil {
			return fmt.Errorf("serve: read the key %q: %w", keyPath, err)
		}
		c, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return fmt.Errorf("serve: read the certificate %q and the key %q: %w", certPath, keyPath, err)
		}
		o.certs = append(o.certs, c)
		return nil
	}
}

// Run serves h until ctx ends, waits Config.DrainDelay, then drains the
// requests in flight and reports once the server has stopped. A cancelled ctx is the ordinary way to stop, so
// Run reports nil for it.
//
// Any certificate among opts turns the server into an HTTPS one, over TLS 1.3
// and h2. A Config.TLSConfig of your own wins, and the certificates join the
// ones it already carries.
//
// Run reports an error for a nil context, a nil handler, a Config that names
// neither an address nor a listener, a negative DrainDelay, a nil or failing
// option, a TLS config with no certificate, a listener it cannot open, and a drain that runs out of
// time. It checks the certificate after OnServer and before it listens.
func Run(ctx context.Context, h http.Handler, cfg Config, opts ...Option) error {
	// Run closes a caller-supplied listener on the serving path, so it owns it
	// from here on and has to close it on every path. It used to return early
	// -- a nil handler, a bad option, a context already cancelled -- with the
	// listener still accepting, and the caller had no way to tell whether it
	// had been taken over or not.
	if cfg.Listener != nil {
		defer cfg.Listener.Close() //nolint:errcheck // Reported by whoever opened it.
	}
	if ctx == nil {
		return errors.New("serve: Run needs a context")
	}
	in, err := prepare(h, cfg, opts)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := in.build(); err != nil {
		return err
	}
	if err := in.open(ctx); err != nil {
		return err
	}
	if cfg.OnListen != nil {
		cfg.OnListen(in.ln.Addr())
	}
	return in.serve(ctx)
}

// instance is one server on its way from a Config to serving, in four steps:
// prepare, build, open and serve.
type instance struct {
	h     http.Handler
	srv   *http.Server
	ln    net.Listener
	certs []tls.Certificate
	cfg   Config
}

func prepare(h http.Handler, cfg Config, opts []Option) (*instance, error) {
	if h == nil {
		return nil, errors.New("serve: the server needs a handler")
	}
	if cfg.Listener == nil && cfg.Addr == "" {
		return nil, errors.New("serve: the server needs Config.Addr or Config.Listener")
	}
	if cfg.DrainDelay < 0 {
		return nil, errors.New("serve: Config.DrainDelay is negative")
	}

	var o options
	for i, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("serve: option %d is nil", i)
		}
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	return &instance{h: h, cfg: cfg, certs: o.certs}, nil
}

func (in *instance) build() error {
	logger := in.cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	in.srv = &http.Server{
		Addr:              in.cfg.Addr,
		Handler:           in.h,
		TLSConfig:         tlsConfig(in.cfg, in.certs),
		ReadHeaderTimeout: headerTimeout(in.cfg.ReadHeaderTimeout),
		ReadTimeout:       in.cfg.ReadTimeout,
		WriteTimeout:      in.cfg.WriteTimeout,
		IdleTimeout:       in.cfg.IdleTimeout,

		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	if in.cfg.OnServer != nil {
		if err := in.cfg.OnServer(in.srv); err != nil {
			return err
		}
	}
	if in.srv.TLSConfig != nil && !hasCertificate(in.srv.TLSConfig) {
		return errors.New("serve: the TLS config has no certificate; add one through Config.TLSConfig, an option or OnServer")
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
		in.ln.Close() //nolint:errcheck // The listener is going away either way.
	}
}

func (in *instance) serve(ctx context.Context) error {
	defer in.closeOwn()

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
		srv.Close() //nolint:errcheck // The drain already failed; the close reports nothing new.
		return fmt.Errorf("serve: the drain did not finish in %s: %w", timeout, err)
	default:
		return fmt.Errorf("serve: shut down: %w", err)
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

func tlsConfig(cfg Config, certs []tls.Certificate) *tls.Config {
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
