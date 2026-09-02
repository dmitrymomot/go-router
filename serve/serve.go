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

const DefaultShutdownTimeout = 10 * time.Second

const DefaultReadHeaderTimeout = 10 * time.Second

type Config struct {
	Addr              string
	Network           string
	Listener          net.Listener
	TLSConfig         *tls.Config
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	Logger            *slog.Logger
	OnListen          func(net.Addr)
	OnServer          func(*http.Server) error
}

type Option func(*options) error

type options struct {
	certs []tls.Certificate
}

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
	if h == nil {
		return errors.New("serve: Run needs a handler")
	}
	if cfg.Listener == nil && cfg.Addr == "" {
		return errors.New("serve: Run needs Config.Addr or Config.Listener")
	}

	var o options
	for i, opt := range opts {
		if opt == nil {
			return fmt.Errorf("serve: option %d is nil", i)
		}
		if err := opt(&o); err != nil {
			return err
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           h,
		TLSConfig:         tlsConfig(cfg, o.certs),
		ReadHeaderTimeout: headerTimeout(cfg.ReadHeaderTimeout),
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,

		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	if cfg.OnServer != nil {
		if err := cfg.OnServer(srv); err != nil {
			return err
		}
	}

	ln, err := listen(ctx, cfg)
	if err != nil {
		return err
	}
	if cfg.Listener == nil {
		defer ln.Close() //nolint:errcheck // The listener is going away either way.
	}

	if cfg.OnListen != nil {
		cfg.OnListen(ln.Addr())
	}

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
		drainErr = drain(ctx, srv, cfg.ShutdownTimeout)
	}()

	err = serveOn(srv, ln)
	var closeErr error
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		if closeErr = srv.Close(); closeErr != nil {
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

func drain(ctx context.Context, srv *http.Server, timeout time.Duration) error {
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
