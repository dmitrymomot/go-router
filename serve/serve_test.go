package serve_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dmitrymomot/go-router/serve"
)

const wait = 5 * time.Second

type server struct {
	addr    string
	cancel  context.CancelFunc
	errc    chan error
	stopped bool
}

func start(tb testing.TB, h http.Handler, cfg serve.Config, opts ...serve.Option) *server {
	tb.Helper()

	if cfg.Addr == "" && cfg.Listener == nil {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	bound := make(chan net.Addr, 1)
	onListen := cfg.OnListen
	cfg.OnListen = func(a net.Addr) {
		if onListen != nil {
			onListen(a)
		}
		bound <- a
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &server{cancel: cancel, errc: make(chan error, 1)}
	go func() { s.errc <- serve.Run(ctx, h, cfg, opts...) }()

	tb.Cleanup(func() {
		if s.stopped {
			return
		}
		cancel()
		select {
		case <-s.errc:
		case <-time.After(wait):
			tb.Error("Run did not return during the cleanup")
		}
	})

	select {
	case a := <-bound:
		s.addr = a.String()
	case err := <-s.errc:
		s.stopped = true
		tb.Fatalf("Run returned before it listened: %v", err)
	case <-time.After(wait):
		tb.Fatal("Run did not listen")
	}
	return s
}

func (s *server) stop(tb testing.TB) error {
	tb.Helper()
	s.cancel()
	select {
	case err := <-s.errc:
		s.stopped = true
		return err
	case <-time.After(wait):
		tb.Fatal("Run did not return after the context was cancelled")
		return nil
	}
}

func (s *server) url(scheme, path string) string {
	return scheme + "://" + s.addr + path
}

func ok() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		//nolint:errcheck // The assertions of the test read what arrived.
		w.Write([]byte("ok"))
	})
}

func client(tb testing.TB) *http.Client {
	tb.Helper()
	tr := &http.Transport{DisableKeepAlives: true}
	tb.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: wait}
}

type result struct {
	err    error
	body   string
	status int
}

func call(cl *http.Client, url string) <-chan result {
	out := make(chan result, 1)
	go func() {
		res, err := cl.Get(url)
		if err != nil {
			out <- result{err: err}
			return
		}
		defer res.Body.Close() //nolint:errcheck // The test is done with it.
		body, err := io.ReadAll(res.Body)
		out <- result{status: res.StatusCode, body: string(body), err: err}
	}()
	return out
}

func await(tb testing.TB, c <-chan result) result {
	tb.Helper()
	select {
	case r := <-c:
		return r
	case <-time.After(wait):
		tb.Fatal("the request never came back")
		return result{}
	}
}

type blocker struct {
	inflight chan struct{}
	release  chan struct{}
	once     sync.Once
	finished atomic.Bool
}

func newBlocker(tb testing.TB) *blocker {
	b := &blocker{inflight: make(chan struct{}), release: make(chan struct{})}
	tb.Cleanup(b.free)
	return b
}

func (b *blocker) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	close(b.inflight)
	<-b.release
	//nolint:errcheck // The assertions of the test read what arrived.
	w.Write([]byte("late"))
	b.finished.Store(true)
}

func (b *blocker) running(tb testing.TB) {
	tb.Helper()
	select {
	case <-b.inflight:
	case <-time.After(wait):
		tb.Fatal("the request never reached the handler")
	}
}

func (b *blocker) free() {
	b.once.Do(func() { close(b.release) })
}

type logLine struct {
	msg   string
	level slog.Level
}

type logRecorder struct {
	lines chan logLine
}

func newLogRecorder() *logRecorder {
	return &logRecorder{lines: make(chan logLine, 8)}
}

func (h *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *logRecorder) Handle(_ context.Context, r slog.Record) error {
	select {
	case h.lines <- logLine{msg: r.Message, level: r.Level}:
	default:
	}
	return nil
}

func (h *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *logRecorder) WithGroup(string) slog.Handler { return h }

func selfSigned(tb testing.TB) (certPEM, keyPEM []byte) {
	tb.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatalf("generate the key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "serve test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatalf("create the certificate: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		tb.Fatalf("marshal the key: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	return certPEM, keyPEM
}

func tlsClient(tb testing.TB, certPEM []byte, http2 bool) *http.Client {
	tb.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		tb.Fatal("the test certificate does not parse")
	}
	tr := &http.Transport{
		DisableKeepAlives: !http2,
		ForceAttemptHTTP2: http2,
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}
	tb.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: wait}
}

func TestRunNeedsAHandler(t *testing.T) {
	if err := serve.Run(context.Background(), nil, serve.Config{Addr: "127.0.0.1:0"}); err == nil {
		t.Fatal("Run accepted a nil handler")
	}
}

func TestRunNeedsAnAddressOrAListener(t *testing.T) {
	if err := serve.Run(context.Background(), ok(), serve.Config{}); err == nil {
		t.Fatal("Run accepted a config with neither Addr nor Listener")
	}
}

func TestRunReportsABindFailure(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("take the address: %v", err)
	}
	defer taken.Close() //nolint:errcheck // The test is done with it.

	listened := false
	err = serve.Run(context.Background(), ok(), serve.Config{
		Addr:     taken.Addr().String(),
		Logger:   slog.New(slog.DiscardHandler),
		OnListen: func(net.Addr) { listened = true },
	})
	if err == nil {
		t.Fatal("Run bound an address that is already in use")
	}
	if listened {
		t.Error("OnListen ran for a bind that failed")
	}
}

func TestRunReportsAnOptionFailure(t *testing.T) {
	listened := false
	err := serve.Run(context.Background(), ok(), serve.Config{
		Addr:     "127.0.0.1:0",
		Logger:   slog.New(slog.DiscardHandler),
		OnListen: func(net.Addr) { listened = true },
	}, serve.CertFiles("absent.pem", "absent.key"))
	if err == nil {
		t.Fatal("Run accepted a certificate that does not load")
	}
	if listened {
		t.Error("OnListen ran for an option that failed")
	}
}

func TestRunReportsTheOnServerFailure(t *testing.T) {
	boom := errors.New("boom")
	listened := false
	err := serve.Run(context.Background(), ok(), serve.Config{
		Addr:     "127.0.0.1:0",
		Logger:   slog.New(slog.DiscardHandler),
		OnListen: func(net.Addr) { listened = true },
		OnServer: func(*http.Server) error { return boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the error of OnServer", err)
	}
	if listened {
		t.Error("OnListen ran after OnServer failed")
	}
}

func TestRunReportsATLSConfigWithoutACertificate(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck // The test closes it twice on purpose.

	err = serve.Run(context.Background(), ok(), serve.Config{
		Listener:  ln,
		Logger:    slog.New(slog.DiscardHandler),
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13},
	})
	if err == nil {
		t.Fatal("Run served TLS without a certificate")
	}
	if errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("err = %v, want the failure of the TLS setup", err)
	}
	if err := ln.Close(); err == nil {
		t.Error("Run left the listener open after the TLS setup failed")
	}
}

func TestRunReturnsNilWhenTheContextIsAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	listened := false
	err := serve.Run(ctx, ok(), serve.Config{
		Addr:     "127.0.0.1:0",
		Logger:   slog.New(slog.DiscardHandler),
		OnListen: func(net.Addr) { listened = true },
	})
	if err != nil {
		t.Fatalf("err = %v, want nil for a context that is already done", err)
	}
	if listened {
		t.Error("Run bound an address for a context that is already done")
	}
}

func TestTimeoutsReachTheServer(t *testing.T) {
	type timeouts struct {
		header, read, write, idle time.Duration
	}

	tests := []struct {
		name string
		cfg  serve.Config
		want timeouts
	}{
		{
			name: "the zero config bounds the headers alone",
			want: timeouts{header: serve.DefaultReadHeaderTimeout},
		},
		{
			name: "every timeout passes through",
			cfg: serve.Config{
				ReadHeaderTimeout: time.Second,
				ReadTimeout:       2 * time.Second,
				WriteTimeout:      3 * time.Second,
				IdleTimeout:       4 * time.Second,
			},
			want: timeouts{header: time.Second, read: 2 * time.Second, write: 3 * time.Second, idle: 4 * time.Second},
		},
		{
			name: "a negative header timeout removes the deadline",
			cfg:  serve.Config{ReadHeaderTimeout: -1},
			want: timeouts{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := captureServer(t, tt.cfg)
			got := timeouts{
				header: srv.ReadHeaderTimeout,
				read:   srv.ReadTimeout,
				write:  srv.WriteTimeout,
				idle:   srv.IdleTimeout,
			}
			if got != tt.want {
				t.Errorf("timeouts = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestWriteTimeoutHasNoDefault(t *testing.T) {
	if got := captureServer(t, serve.Config{}).WriteTimeout; got != 0 {
		t.Fatalf("WriteTimeout = %s, want no default", got)
	}
}

func TestTheErrorLogWritesToTheConfiguredLogger(t *testing.T) {
	rec := newLogRecorder()
	srv := captureServer(t, serve.Config{Logger: slog.New(rec)})
	if srv.ErrorLog == nil {
		t.Fatal("the server has no ErrorLog")
	}

	srv.ErrorLog.Print("http: something went wrong")
	select {
	case line := <-rec.lines:
		if line.level != slog.LevelError {
			t.Errorf("level = %v, want %v", line.level, slog.LevelError)
		}
		if line.msg != "http: something went wrong" {
			t.Errorf("msg = %q, want the line of the server", line.msg)
		}
	default:
		t.Fatal("the ErrorLog wrote nothing to the handler")
	}
}

func captureServer(tb testing.TB, cfg serve.Config, opts ...serve.Option) *http.Server {
	tb.Helper()

	stop := errors.New("stop before the bind")
	var srv *http.Server
	cfg.Addr = "127.0.0.1:0"
	cfg.OnServer = func(s *http.Server) error {
		srv = s
		return stop
	}
	if err := serve.Run(context.Background(), ok(), cfg, opts...); !errors.Is(err, stop) {
		tb.Fatalf("Run = %v, want the error of OnServer", err)
	}
	if srv == nil {
		tb.Fatal("OnServer never ran")
	}
	return srv
}

func TestRunServesRequests(t *testing.T) {
	s := start(t, ok(), serve.Config{})

	res := await(t, call(client(t), s.url("http", "/")))
	if res.err != nil {
		t.Fatalf("request: %v", res.err)
	}
	if res.status != http.StatusOK || res.body != "ok" {
		t.Fatalf("answer = %d %q, want 200 %q", res.status, res.body, "ok")
	}
}

func TestRunReturnsNilWhenTheContextIsCancelled(t *testing.T) {
	s := start(t, ok(), serve.Config{})

	if err := s.stop(t); err != nil {
		t.Fatalf("err = %v, want nil for a shutdown that the caller asked for", err)
	}
	if res := await(t, call(client(t), s.url("http", "/"))); res.err == nil {
		t.Fatalf("the server answered %d after the shutdown", res.status)
	}
}

func TestOnListenReceivesTheBoundAddress(t *testing.T) {
	var got net.Addr
	s := start(t, ok(), serve.Config{
		Addr:     "127.0.0.1:0",
		OnListen: func(a net.Addr) { got = a },
	})

	if got == nil {
		t.Fatal("OnListen never ran")
	}
	if got.Network() != "tcp" {
		t.Errorf("network = %q, want tcp by default", got.Network())
	}
	_, port, err := net.SplitHostPort(got.String())
	if err != nil {
		t.Fatalf("split the address: %v", err)
	}
	if port == "0" {
		t.Fatal("OnListen reported the port of the config, not the one it bound")
	}
	if res := await(t, call(client(t), s.url("http", "/"))); res.err != nil {
		t.Fatalf("request to the address of OnListen: %v", res.err)
	}
}

func TestRunServesTheListenerOfTheConfig(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	s := start(t, ok(), serve.Config{Addr: "127.0.0.1:1", Network: "unix", Listener: ln})
	if s.addr != ln.Addr().String() {
		t.Fatalf("addr = %s, want the address of the listener %s", s.addr, ln.Addr())
	}
	if res := await(t, call(client(t), s.url("http", "/"))); res.err != nil {
		t.Fatalf("request: %v", res.err)
	}

	if err := s.stop(t); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := ln.Close(); err == nil {
		t.Error("Run left the listener of the config open")
	}
}

func TestRunWaitsForTheDrainToFinish(t *testing.T) {
	b := newBlocker(t)
	s := start(t, b, serve.Config{})

	answer := call(client(t), s.url("http", "/"))
	b.running(t)

	time.AfterFunc(100*time.Millisecond, b.free)

	if err := s.stop(t); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !b.finished.Load() {
		t.Fatal("Run returned while a request was still in flight")
	}

	res := await(t, answer)
	if res.err != nil {
		t.Fatalf("the request in flight failed: %v", res.err)
	}
	if res.body != "late" {
		t.Fatalf("body = %q, want the answer that the drain waited for", res.body)
	}
}

func TestTheDrainReportsItsTimeout(t *testing.T) {
	b := newBlocker(t)
	s := start(t, b, serve.Config{ShutdownTimeout: 50 * time.Millisecond})

	answer := call(client(t), s.url("http", "/"))
	b.running(t)

	err := s.stop(t)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline of the drain", err)
	}
	if b.finished.Load() {
		t.Fatal("the handler finished; the test proves nothing about the timeout")
	}
	if res := await(t, answer); res.err == nil {
		t.Fatalf("the request survived the timeout with %d %q", res.status, res.body)
	}
}

func TestANegativeShutdownTimeoutSkipsTheDrain(t *testing.T) {
	b := newBlocker(t)
	s := start(t, b, serve.Config{ShutdownTimeout: -1})

	answer := call(client(t), s.url("http", "/"))
	b.running(t)

	if err := s.stop(t); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if b.finished.Load() {
		t.Fatal("Run waited for the request in flight")
	}
	if res := await(t, answer); res.err == nil {
		t.Fatalf("the request survived the close with %d %q", res.status, res.body)
	}
}

type errListener struct {
	net.Listener
	err error
}

func (l errListener) Close() error {
	l.Listener.Close() //nolint:errcheck // The test wants the error below instead.
	return l.err
}

func TestRunReportsAShutdownFailure(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
	}{
		{name: "the drain reports it", timeout: 0},
		{name: "the close that skips the drain reports it", timeout: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			boom := errors.New("boom")
			s := start(t, ok(), serve.Config{
				Listener:        errListener{Listener: ln, err: boom},
				ShutdownTimeout: tt.timeout,
			})

			if res := await(t, call(client(t), s.url("http", "/"))); res.err != nil {
				t.Fatalf("request: %v", res.err)
			}

			if err := s.stop(t); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the failure of the close", err)
			}
		})
	}
}

func TestOnServerRegistersAShutdownHook(t *testing.T) {
	shutdown := make(chan struct{})
	s := start(t, ok(), serve.Config{
		OnServer: func(srv *http.Server) error {
			srv.RegisterOnShutdown(func() { close(shutdown) })
			return nil
		},
	})

	if err := s.stop(t); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case <-shutdown:
	case <-time.After(wait):
		t.Fatal("the drain never ran the hook that OnServer registered")
	}
}

func TestServesOverTLS(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	for path, data := range map[string][]byte{certPath: certPEM, keyPath: keyPEM} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	fsys := fstest.MapFS{
		"tls/cert.pem": {Data: certPEM},
		"tls/key.pem":  {Data: keyPEM},
	}

	tests := []struct {
		opt  serve.Option
		name string
	}{
		{name: "CertFiles", opt: serve.CertFiles(certPath, keyPath)},
		{name: "CertPEM", opt: serve.CertPEM(certPEM, keyPEM)},
		{name: "CertFS", opt: serve.CertFS(fsys, "tls/cert.pem", "tls/key.pem")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := start(t, ok(), serve.Config{}, tt.opt)

			res := await(t, call(tlsClient(t, certPEM, false), s.url("https", "/")))
			if res.err != nil {
				t.Fatalf("request: %v", res.err)
			}
			if res.status != http.StatusOK || res.body != "ok" {
				t.Fatalf("answer = %d %q, want 200 %q", res.status, res.body, "ok")
			}
		})
	}
}

func TestTLSFloorIsTLS13(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	cfg := captureServer(t, serve.Config{}, serve.CertPEM(certPEM, keyPEM)).TLSConfig

	if cfg == nil {
		t.Fatal("a certificate option left the server without a TLS config")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want TLS 1.3 (%#x)", cfg.MinVersion, tls.VersionTLS13)
	}
	if want := []string{"h2", "http/1.1"}; !slices.Equal(cfg.NextProtos, want) {
		t.Errorf("NextProtos = %v, want %v", cfg.NextProtos, want)
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("certificates = %d, want the one of the option", len(cfg.Certificates))
	}
}

func TestATLS12ClientIsRefused(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	s := start(t, ok(), serve.Config{}, serve.CertPEM(certPEM, keyPEM))

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("the test certificate does not parse")
	}
	tr := &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS12,
		},
	}
	defer tr.CloseIdleConnections()

	cl := &http.Client{Transport: tr, Timeout: wait}
	if res := await(t, call(cl, s.url("https", "/"))); res.err == nil {
		t.Fatalf("a TLS 1.2 client got %d %q", res.status, res.body)
	}
}

func TestTLSOffersHTTP2(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	s := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//nolint:errcheck // The assertions of the test read what arrived.
		io.WriteString(w, r.Proto)
	}), serve.Config{}, serve.CertPEM(certPEM, keyPEM))

	res := await(t, call(tlsClient(t, certPEM, true), s.url("https", "/")))
	if res.err != nil {
		t.Fatalf("request: %v", res.err)
	}
	if res.body != "HTTP/2.0" {
		t.Fatalf("proto = %q, want HTTP/2.0 from the h2 of NextProtos", res.body)
	}
}

func TestAnExplicitTLSConfigWins(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	own := &tls.Config{MinVersion: tls.VersionTLS12}

	got := captureServer(t, serve.Config{TLSConfig: own}, serve.CertPEM(certPEM, keyPEM)).TLSConfig

	if got.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want the one of the config (%#x)", got.MinVersion, tls.VersionTLS12)
	}
	if got.NextProtos != nil {
		t.Errorf("NextProtos = %v, want the ones of the config", got.NextProtos)
	}
	if len(got.Certificates) != 1 {
		t.Errorf("certificates = %d, want the one of the option", len(got.Certificates))
	}
	if len(own.Certificates) != 0 {
		t.Error("Run wrote the certificate onto the config of the caller")
	}
}

func TestCertOptionsReportAFailure(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	fsys := fstest.MapFS{
		"cert.pem": {Data: certPEM},
		"key.pem":  {Data: keyPEM},
		"junk.pem": {Data: []byte("not a certificate")},
	}

	tests := []struct {
		opt  serve.Option
		name string
	}{
		{name: "a certificate file that is not there", opt: serve.CertFiles("absent.pem", "absent.key")},
		{name: "PEM blocks that do not parse", opt: serve.CertPEM([]byte("cert"), []byte("key"))},
		{name: "PEM blocks that do not match", opt: serve.CertPEM(certPEM, []byte("key"))},
		{name: "a certificate that the file system does not hold", opt: serve.CertFS(fsys, "absent.pem", "key.pem")},
		{name: "a key that the file system does not hold", opt: serve.CertFS(fsys, "cert.pem", "absent.key")},
		{name: "a file that holds no certificate", opt: serve.CertFS(fsys, "junk.pem", "key.pem")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := serve.Run(context.Background(), ok(), serve.Config{
				Addr:   "127.0.0.1:0",
				Logger: slog.New(slog.DiscardHandler),
			}, tt.opt)
			if err == nil {
				t.Fatal("Run accepted the certificate")
			}
			if !strings.HasPrefix(err.Error(), "serve: ") {
				t.Errorf("err = %v, want an error that names the package", err)
			}
		})
	}
}

func TestHandshakeFailuresReachTheLogger(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	rec := newLogRecorder()
	s := start(t, ok(), serve.Config{Logger: slog.New(rec)}, serve.CertPEM(certPEM, keyPEM))

	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte("hello, is this tls?\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.Close() //nolint:errcheck // The test is done with it.

	select {
	case line := <-rec.lines:
		if line.level != slog.LevelError {
			t.Errorf("level = %v, want %v", line.level, slog.LevelError)
		}
		if !strings.Contains(line.msg, "TLS handshake error") {
			t.Errorf("msg = %q, want the handshake failure", line.msg)
		}
	case <-time.After(wait):
		t.Fatal("the failed handshake reached no logger")
	}
}

func Example() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		//nolint:errcheck // The example shows the shape of a server, not error handling.
		fmt.Fprint(w, "ok")
	})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	bound := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve.Run(ctx, mux, serve.Config{
			Addr:     "127.0.0.1:0",
			OnListen: func(a net.Addr) { bound <- a },
		})
	}()

	res, err := http.Get("http://" + (<-bound).String() + "/health")
	if err != nil {
		fmt.Println(err)
		return
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close() //nolint:errcheck // The example is done with it.
	fmt.Println(res.StatusCode, string(body))

	stop()
	fmt.Println(<-done)
	// Output:
	// 200 ok
	// <nil>
}
