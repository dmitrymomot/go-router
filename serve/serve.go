// Package serve runs an HTTP server that stops when its context does.
//
// [Run] binds the address, serves the handler, and drains the requests that
// are still in flight once the context is cancelled:
//
//	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
//	defer stop()
//
//	if err := serve.Run(ctx, r, serve.Config{Addr: ":8080"}); err != nil {
//		log.Fatal(err)
//	}
//
// The package takes an [http.Handler], so it serves a router, a scope, a
// mounted subsystem or any standard handler, and it never sees the context
// type of the router.
//
// A stop is not a failure: Run returns nil once the drain is over. Every other
// end is an error, be it an address in use, a certificate that does not load,
// or a drain that ran out of time.
//
// Serve HTTPS by passing a certificate:
//
//	err := serve.Run(ctx, r, serve.Config{Addr: ":443"}, serve.CertFiles("cert.pem", "key.pem"))
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

// DefaultShutdownTimeout is the time that the drain takes at most when
// [Config.ShutdownTimeout] is zero.
const DefaultShutdownTimeout = 10 * time.Second

// DefaultReadHeaderTimeout is the header deadline that [Run] applies when
// [Config.ReadHeaderTimeout] is zero.
const DefaultReadHeaderTimeout = 10 * time.Second

// Config describes the server that [Run] starts. The zero value needs an Addr
// or a Listener and nothing else.
type Config struct {
	// Addr is the address to bind, in the form that [net.Listen] takes, such
	// as ":8080" or "127.0.0.1:8080". A port of zero binds an ephemeral one,
	// which OnListen then reports.
	//
	// Listener replaces it. Run needs one of the two.
	Addr string

	// Network is the network to bind, one of the stream networks of
	// [net.Listen]: "tcp", "tcp4", "tcp6" or "unix". It defaults to "tcp".
	Network string

	// Listener serves the requests when it is set, and Addr and Network then
	// say nothing. Use it for a socket that the process inherited, or for one
	// that a test bound itself.
	//
	// Run serves on it and closes it, as [http.Server.Serve] does. A Run that
	// returns before it serves leaves it open.
	Listener net.Listener

	// TLSConfig serves HTTPS. It is used as it stands, so a config given here
	// keeps its own minimum version and protocols, and the certificate
	// options of Run only add to its certificates.
	//
	// Leave it nil and pass [CertFiles], [CertPEM] or [CertFS] instead to get
	// the defaults of this package: TLS 1.3 as the floor, and HTTP/2 offered
	// ahead of HTTP/1.1.
	TLSConfig *tls.Config

	// ReadHeaderTimeout bounds the time a connection may take to send the
	// request headers. It defaults to [DefaultReadHeaderTimeout], and a
	// negative value removes it.
	//
	// This is the one deadline that the package sets for you, because it
	// covers the header phase alone. A connection that opens and then
	// trickles bytes holds a goroutine for as long as it likes, while no
	// client needs seconds to write its headers, however long its body or the
	// answer to it takes.
	ReadHeaderTimeout time.Duration

	// ReadTimeout bounds the time from the start of the connection to the end
	// of the request body. It has no default, because the body of an upload
	// is as slow as the link that carries it.
	ReadTimeout time.Duration

	// WriteTimeout bounds the time from the end of the request headers to the
	// end of the response. It has no default, and it is the one field to
	// reach for last.
	//
	// The deadline covers the whole response, so it cuts a long download, a
	// slow report and a server-sent event stream off in the middle, and the
	// client reads a truncated body rather than an error. The SSE writer of
	// the router clears the deadline for the stream it opens; nothing else
	// does. Bound the header phase with ReadHeaderTimeout instead, which no
	// long answer can outlive, and put a deadline on the work of a handler
	// with the timeout middleware, which knows what the handler is doing.
	WriteTimeout time.Duration

	// IdleTimeout is how long a keep-alive connection may sit between
	// requests. It has no default, so an idle connection lives until the
	// client or the network drops it.
	IdleTimeout time.Duration

	// ShutdownTimeout is how long the drain may take. It defaults to
	// [DefaultShutdownTimeout]. A negative value skips the drain and closes
	// every connection at once.
	//
	// The drain waits for the running handlers to return and does not cancel
	// their request contexts, which is what makes it graceful: a request that
	// is halfway through a transaction finishes it. A handler that runs until
	// something tells it to stop, such as a stream or a long poll, therefore
	// holds the drain until the timeout unless it has a signal of its own;
	// [http.Server.RegisterOnShutdown] from OnServer is that signal. A drain
	// that runs out of time closes what is left, and Run reports the timeout.
	ShutdownTimeout time.Duration

	// Logger receives what the server reports on its own: a TLS handshake
	// that failed, a request that never parsed, a panic in a handler that no
	// middleware recovered. It defaults to [slog.Default], and the records
	// land at [slog.LevelError].
	//
	// Those lines otherwise go to the standard logger of the process, which
	// writes plain text to stderr and misses every attribute that the
	// application put on its own handler.
	Logger *slog.Logger

	// OnListen receives the bound address, once, before the first request.
	// The library logs nothing itself, so this is where an application says
	// that it is up:
	//
	//	OnListen: func(a net.Addr) { slog.Info("listening", "addr", a) }
	//
	// The address is the one the socket holds, not the one the config asked
	// for, so an Addr of ":0" becomes a port that a test can then reach.
	OnListen func(net.Addr)

	// OnServer receives the server after Run has filled it in and before it
	// binds, for the fields that this config does not carry:
	//
	//	OnServer: func(s *http.Server) error {
	//		s.MaxHeaderBytes = 1 << 20
	//		s.RegisterOnShutdown(func() { close(stop) })
	//		return nil
	//	}
	//
	// An error it returns stops Run before anything binds.
	OnServer func(*http.Server) error
}

// Option adds a TLS certificate to the server that [Run] starts. One
// certificate serves HTTPS; several let the handshake pick by name and by key
// type, as [tls.Config.Certificates] does.
type Option func(*options) error

// options holds what the [Option] values contribute.
type options struct {
	certs []tls.Certificate
}

// CertFiles reads a certificate and its key from two files on disk.
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

// CertPEM reads a certificate and its key from PEM blocks that the caller
// already holds, such as ones a secret store answered with.
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

// CertFS reads a certificate and its key from a file system, normally an
// embed.FS, so that a single binary carries the certificate of an internal
// service.
func CertFS(fsys fs.FS, certPath, keyPath string) Option {
	return func(o *options) error {
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

// Run serves h until ctx is cancelled, then drains the requests that are still
// in flight and returns.
//
// It blocks for as long as the server runs. A cancelled context closes the
// listener, so no new request arrives, and gives the running handlers
// [Config.ShutdownTimeout] to finish; Run returns once the last of them has,
// which is the point at which the process may exit.
//
// It returns nil for the shutdown that the context asked for. It returns an
// error for everything else: a config it cannot serve, an option that failed,
// an address it cannot bind, and a drain that ran out of time. A context that
// is already done when Run is called serves nothing and returns nil, so a
// server that starts inside a group whose context has failed reports no
// failure of its own.
func Run(ctx context.Context, h http.Handler, cfg Config, opts ...Option) error {
	if h == nil {
		return errors.New("serve: Run needs a handler")
	}
	if cfg.Listener == nil && cfg.Addr == "" {
		return errors.New("serve: Run needs Config.Addr or Config.Listener")
	}

	var o options
	for _, opt := range opts {
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

		// The server writes its own errors here: a failed handshake, a
		// malformed request, a panic that no middleware recovered. The handler
		// of the application keeps them in the stream that the application
		// already reads, with the same format and the same attributes as the
		// records of a request.
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
	// Serve closes the listener itself. ServeTLS can fail before it reaches
	// Serve, which is the one path that would leave it open; a second close
	// only reports that it is closed already.
	defer ln.Close() //nolint:errcheck // The listener is going away either way.

	if cfg.OnListen != nil {
		cfg.OnListen(ln.Addr())
	}

	// stopped reports that Serve returned, which releases the drain goroutine
	// when the server stopped for a reason of its own. drained reports that
	// the goroutine is done, and Run waits for it: a Serve that returns as
	// soon as Shutdown closes the listener says nothing about the requests
	// that Shutdown is still waiting for.
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
	close(stopped)
	<-drained

	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if err != nil {
		return err
	}
	return drainErr
}

// listen binds the address of the config, or hands back the listener that the
// caller bound.
func listen(ctx context.Context, cfg Config) (net.Listener, error) {
	if cfg.Listener != nil {
		return cfg.Listener, nil
	}
	network := cfg.Network
	if network == "" {
		network = "tcp"
	}
	// The context aborts a bind that has to resolve a name first, which is
	// the part of a bind that waits on the network.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, network, cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("serve: listen on %s %s: %w", network, cfg.Addr, err)
	}
	return ln, nil
}

// serveOn runs the server, over TLS when the config carries one.
func serveOn(srv *http.Server, ln net.Listener) error {
	if srv.TLSConfig != nil {
		return srv.ServeTLS(ln, "", "")
	}
	return srv.Serve(ln)
}

// drain stops the server and waits for the requests that are in flight.
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

	// The drain outlives the context that started it. ctx is already done by
	// the time this runs, and Shutdown on a context that is done returns at
	// once and drains nothing.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	switch err := srv.Shutdown(sctx); {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		// Shutdown leaves the connections it did not drain open. Close them,
		// so that the timeout bounds Run and not only the waiting.
		srv.Close() //nolint:errcheck // The drain already failed; the close reports nothing new.
		return fmt.Errorf("serve: the drain did not finish in %s: %w", timeout, err)
	default:
		return fmt.Errorf("serve: shut down: %w", err)
	}
}

// headerTimeout returns the header deadline of the server. Zero takes the
// default; a negative value removes the deadline, which the server spells as
// zero.
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

// tlsConfig returns the TLS config of the server, or nil to serve plain HTTP.
//
// A config that the caller supplied stands as it is, because a floor that the
// package raises behind the back of an explicit config is a floor that nobody
// can lower. The certificates of the options are added to it either way.
func tlsConfig(cfg Config, certs []tls.Certificate) *tls.Config {
	if cfg.TLSConfig == nil {
		if len(certs) == 0 {
			return nil
		}
		return &tls.Config{
			// TLS 1.3 is eight years old and every current client speaks it.
			// A library that ships in 2026 has no reason to offer less.
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h2", "http/1.1"},
			Certificates: certs,
		}
	}

	c := cfg.TLSConfig.Clone()
	if len(certs) > 0 {
		// Concat rather than append: Clone copies the slice header, so an
		// append could write into the array of the caller.
		c.Certificates = slices.Concat(c.Certificates, certs)
	}
	return c
}
