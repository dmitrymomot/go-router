package ws

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/internal/drain"
)

func TestAcceptKey(t *testing.T) {
	if got := acceptKey(sampleKey); got != sampleAccept {
		t.Errorf("acceptKey = %q, want %q", got, sampleAccept)
	}
}

func TestHandshake(t *testing.T) {
	srv := server(t, func(c *tctx) error {
		c.Response().Header().Set("Set-Cookie", "a=b")
		c.Response().Header().Set(router.HeaderContentType, "text/plain")
		conn, err := Accept(c, Subprotocols("v2", "v1"))
		if err != nil {
			return err
		}
		if conn.Request().URL.Path != "/ws" {
			t.Errorf("Request path = %q", conn.Request().URL.Path)
		}
		return conn.Write(c, Text, []byte(conn.Subprotocol()))
	})
	h := upgradeHeader()
	h.Add(HeaderSecWebSocketProtocol, "v0, v1")
	h.Add(HeaderSecWebSocketProtocol, "v2")
	c := handshake(t, srv, h)
	if c.resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", c.resp.StatusCode)
	}
	for name, want := range map[string]string{
		HeaderSecWebSocketAccept:   sampleAccept,
		HeaderSecWebSocketProtocol: "v2",
		"Upgrade":                  "websocket",
		"Connection":               "Upgrade",
		"Set-Cookie":               "a=b",
		router.HeaderContentType:   "",
	} {
		if got := c.resp.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if f := c.mustRecv(); f.op != opText || string(f.p) != "v2" {
		t.Errorf("frame = %#x %q, want the subprotocol", f.op, f.p)
	}
	// The handler returned without Close.
	c.expectClose(StatusGoingAway)
	c.expectEOF()
}

func TestHandshakeNoCommonSubprotocol(t *testing.T) {
	srv := server(t, func(c *tctx) error {
		conn, err := Accept(c, Subprotocols("v1"))
		if err != nil {
			return err
		}
		return conn.Write(c, Text, []byte(conn.Subprotocol()))
	})
	h := upgradeHeader()
	h.Set(HeaderSecWebSocketProtocol, "v3, ,")
	c := handshake(t, srv, h)
	if got := c.resp.Header.Get(HeaderSecWebSocketProtocol); got != "" {
		t.Errorf("subprotocol = %q, want none", got)
	}
	if f := c.mustRecv(); len(f.p) != 0 {
		t.Errorf("Subprotocol = %q, want empty", f.p)
	}
}

func TestHandshakeRefusals(t *testing.T) {
	srv := server(t, echo(nil, Origins("https://*.example.com", "HTTPS://Other.test")))
	cases := []struct {
		edit   func(h http.Header)
		header string
		value  string
		name   string
		status int
	}{
		{name: "no upgrade", edit: func(h http.Header) { h.Del("Upgrade") }, status: 426, header: "Upgrade", value: "websocket"},
		{name: "version", edit: func(h http.Header) { h.Set(HeaderSecWebSocketVersion, "8") }, status: 426, header: HeaderSecWebSocketVersion, value: "13"},
		{name: "no key", edit: func(h http.Header) { h.Del(HeaderSecWebSocketKey) }, status: 400},
		{name: "short key", edit: func(h http.Header) { h.Set(HeaderSecWebSocketKey, "c2hvcnQ=") }, status: 400},
		{name: "bad key", edit: func(h http.Header) { h.Set(HeaderSecWebSocketKey, "!!!") }, status: 400},
		{name: "foreign origin", edit: func(h http.Header) { h.Set("Origin", "https://evil.test") }, status: 403},
		{name: "bad origin", edit: func(h http.Header) { h.Set("Origin", "::") }, status: 403},
		{name: "null origin", edit: func(h http.Header) { h.Set("Origin", "null") }, status: 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := upgradeHeader()
			tc.edit(h)
			c := handshake(t, srv, h)
			if c.resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", c.resp.StatusCode, tc.status)
			}
			if tc.header != "" && c.resp.Header.Get(tc.header) != tc.value {
				t.Errorf("%s = %q, want %q", tc.header, c.resp.Header.Get(tc.header), tc.value)
			}
		})
	}
}

func TestHandshakeOrigins(t *testing.T) {
	srv := server(t, echo(nil, Origins("https://*.example.com", "HTTPS://Other.test")))
	host := strings.TrimPrefix(srv.URL, "http://")
	for _, origin := range []string{"", "http://" + host, "https://" + strings.ToUpper(host), "https://a.example.com", "https://other.TEST"} {
		h := upgradeHeader()
		if origin != "" {
			h.Set("Origin", origin)
		}
		if c := handshake(t, srv, h); c.resp.StatusCode != http.StatusSwitchingProtocols {
			t.Errorf("Origin %q: status = %d, want 101", origin, c.resp.StatusCode)
		}
	}

	any := server(t, echo(nil, Origins("*")))
	h := upgradeHeader()
	h.Set("Origin", "https://anything.test")
	if c := handshake(t, any, h); c.resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("Origins(*): status = %d, want 101", c.resp.StatusCode)
	}
}

func TestHandshakeMethod(t *testing.T) {
	srv := server(t, echo(nil))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/ws", nil)
	req.Header = upgradeHeader()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET" {
		t.Errorf("status = %d, Allow = %q, want 405 and GET", resp.StatusCode, resp.Header.Get("Allow"))
	}
}

// serveRecorder runs h for req on a recorder, which cannot be hijacked.
func serveRecorder(h router.HandlerFunc[*tctx], req *http.Request) *httptest.ResponseRecorder {
	r := newRouter()
	r.GET("/ws", h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func upgradeRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header = upgradeHeader()
	return req
}

func TestAcceptFailsOnTheServerSide(t *testing.T) {
	var accErr error
	h := func(opts ...Option) router.HandlerFunc[*tctx] {
		return func(c *tctx) error {
			_, accErr = Accept(c, opts...)
			return accErr
		}
	}

	if rec := serveRecorder(h(), upgradeRequest()); rec.Code != 500 || !strings.Contains(accErr.Error(), "take the connection") {
		t.Errorf("recorder: status = %d, err = %v, want 500 for a writer that cannot be hijacked", rec.Code, accErr)
	}
	if rec := serveRecorder(h(nil), upgradeRequest()); rec.Code != 500 {
		t.Errorf("nil option: status = %d, want 500", rec.Code)
	}

	req := upgradeRequest()
	req.ProtoMajor, req.ProtoMinor = 2, 0
	if rec := serveRecorder(h(), req); rec.Code != 500 || !strings.Contains(accErr.Error(), "HTTP/2.0") {
		t.Errorf("HTTP/2: status = %d, err = %v, want 500", rec.Code, accErr)
	}

	serveRecorder(func(c *tctx) error {
		c.Response().WriteHeader(http.StatusOK)
		_, accErr = Accept(c)
		return nil
	}, upgradeRequest())
	if accErr == nil || !strings.Contains(accErr.Error(), "went out") {
		t.Errorf("committed: err = %v, want the response that went out", accErr)
	}
}

// hidden is a context that does not carry its Base. Accept calls no other
// method of it.
type hidden struct {
	router.Context
	res *router.Response
	req *http.Request
}

func (h hidden) Value(any) any              { return nil }
func (h hidden) Response() *router.Response { return h.res }
func (h hidden) Request() *http.Request     { return h.req }

func TestAcceptWithoutABase(t *testing.T) {
	errs := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := &router.Response{ResponseWriter: w}
		conn, err := Accept(hidden{res: res, req: r}) //nolint:contextcheck // The point is a context without the Base.
		if err != nil {
			errs <- err
			return
		}
		_, _, err = conn.Read(r.Context())
		errs <- err
		// No router serves this Base, so the handler closes by hand.
		_ = conn.Close(StatusNormalClosure, "") //nolint:contextcheck // Close takes no context.
	}))
	defer srv.Close()
	c := dial(t, srv)
	c.send(opClose, closePayload(StatusNormalClosure, ""))
	c.expectClose(StatusNormalClosure)
	if err := <-errs; CloseStatus(err) != StatusNormalClosure {
		t.Errorf("Read = %v, want the close of the client", err)
	}
}

func TestEcho(t *testing.T) {
	srv := server(t, echo(nil, ReadLimit(1<<20)))
	c := dial(t, srv)
	for _, tc := range []struct {
		p  []byte
		op byte
	}{
		{op: opText, p: []byte("hello")},
		{op: opText, p: nil},
		{op: opBinary, p: []byte{0, 1, 2, 0xFF}},
		{op: opBinary, p: bytes.Repeat([]byte("x"), 200)},
		{op: opText, p: bytes.Repeat([]byte("y"), 70000)},
	} {
		c.send(tc.op, tc.p)
		f := c.mustRecv()
		if !f.fin || f.op != tc.op || !bytes.Equal(f.p, tc.p) {
			t.Errorf("echo of %d bytes of %#x = fin %v, %#x, %d bytes", len(tc.p), tc.op, f.fin, f.op, len(f.p))
		}
	}
}

func TestFragmentsAndControlFrames(t *testing.T) {
	srv := server(t, echo(nil))
	c := dial(t, srv)
	euro := []byte("€") // three bytes, split across frames
	c.write(
		frame(false, opText, []byte("a")),
		frame(false, opContinuation, euro[:1]),
		frame(true, opPing, []byte("p")),
		frame(false, opContinuation, nil),
		frame(false, opContinuation, euro[1:2]),
		frame(true, opPong, []byte("unsolicited")),
		frame(true, opContinuation, append(euro[2:], 'b')),
	)
	if f := c.mustRecv(); f.op != opPong || string(f.p) != "p" {
		t.Fatalf("frame = %#x %q, want the pong", f.op, f.p)
	}
	if f := c.mustRecv(); f.op != opText || string(f.p) != "a€b" {
		t.Fatalf("frame = %#x %q, want the joined message", f.op, f.p)
	}
}

func TestProtocolViolations(t *testing.T) {
	cases := []struct {
		name   string
		frames [][]byte
		code   StatusCode
	}{
		{"unmasked", [][]byte{rawFrame(true, 0, opText, false, []byte("x"))}, StatusProtocolError},
		{"reserved bit", [][]byte{rawFrame(true, 4, opText, true, []byte("x"))}, StatusProtocolError},
		{"reserved opcode", [][]byte{frame(true, 0x3, nil)}, StatusProtocolError},
		{"reserved control opcode", [][]byte{frame(true, 0xB, nil)}, StatusProtocolError},
		{"fragmented control", [][]byte{frame(false, opPing, nil)}, StatusProtocolError},
		{"long control", [][]byte{frame(true, opPing, make([]byte, 126))}, StatusProtocolError},
		{"lone continuation", [][]byte{frame(true, opContinuation, []byte("x"))}, StatusProtocolError},
		{"data inside a message", [][]byte{frame(false, opText, []byte("x")), frame(true, opText, []byte("y"))}, StatusProtocolError},
		{"length bit 63", [][]byte{{finBit | opBinary, maskBit | 127, 0x80, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}}, StatusProtocolError},
		{"invalid UTF-8", [][]byte{frame(true, opText, []byte{0xFF})}, StatusInvalidPayload},
		{"invalid UTF-8 in a fragment", [][]byte{frame(false, opText, []byte{'a', 0xC0, 0x80})}, StatusInvalidPayload},
		{"truncated UTF-8", [][]byte{frame(true, opText, []byte{0xE2, 0x82})}, StatusInvalidPayload},
		{"too big", [][]byte{frame(true, opBinary, make([]byte, 101))}, StatusMessageTooBig},
		{"too big in fragments", [][]byte{frame(false, opBinary, make([]byte, 60)), frame(true, opContinuation, make([]byte, 60))}, StatusMessageTooBig},
		{"one-byte close", [][]byte{frame(true, opClose, []byte{3})}, StatusProtocolError},
		{"close code 999", [][]byte{frame(true, opClose, closePayload(999, ""))}, StatusProtocolError},
		{"close code 1005", [][]byte{frame(true, opClose, closePayload(1005, ""))}, StatusProtocolError},
		{"close code 5000", [][]byte{frame(true, opClose, closePayload(5000, ""))}, StatusProtocolError},
		{"close reason not UTF-8", [][]byte{frame(true, opClose, closePayload(1000, "\xFF"))}, StatusInvalidPayload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := make(chan error, 1)
			srv := server(t, echo(errs, ReadLimit(100)))
			c := dial(t, srv)
			c.write(tc.frames...)
			c.expectClose(tc.code)
			c.expectEOF()
			err := <-errs
			if _, ok := errors.AsType[*protocolError](err); !ok {
				t.Errorf("Read = %v, want a protocol error", err)
			}
		})
	}
}

func TestClientCloses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
		code    StatusCode
		reason  string
	}{
		{"code and reason", closePayload(StatusGoingAway, "bye"), StatusGoingAway, "bye"},
		{"library code", closePayload(3000, ""), 3000, ""},
		{"no code", nil, StatusNoStatusReceived, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := make(chan error, 1)
			srv := server(t, echo(errs))
			c := dial(t, srv)
			c.send(opClose, tc.payload)
			echoed := tc.code
			if tc.code == StatusNoStatusReceived {
				echoed = 0
			}
			c.expectClose(echoed)
			c.expectEOF()
			err := <-errs
			ce, ok := errors.AsType[*CloseError](err)
			if !ok || ce.Code != tc.code || ce.Reason != tc.reason {
				t.Fatalf("Read = %v, want code %d and reason %q", err, tc.code, tc.reason)
			}
			if !strings.Contains(err.Error(), "closed the connection") {
				t.Errorf("Error = %q", err)
			}
		})
	}
}

func TestServerCloses(t *testing.T) {
	results := make(chan []error, 1)
	srv := server(t, func(c *tctx) error {
		conn, err := Accept(c)
		if err != nil {
			return err
		}
		var errs []error
		errs = append(errs, conn.Close(StatusPolicyViolation, "rules"))
		errs = append(errs, conn.Close(StatusNormalClosure, ""))
		_, _, rerr := conn.Read(c)
		errs = append(errs, rerr)
		errs = append(errs, conn.Write(c, Text, []byte("late")))
		select {
		case <-conn.Done():
		default:
			errs = append(errs, errors.New("Done is open after Close"))
		}
		results <- errs
		return nil
	})
	c := dial(t, srv)
	if reason := c.expectClose(StatusPolicyViolation); reason != "rules" {
		t.Errorf("reason = %q, want rules", reason)
	}
	c.send(opClose, closePayload(StatusPolicyViolation, ""))
	c.expectEOF()
	errs := <-results
	if errs[0] != nil || errs[1] != nil {
		t.Errorf("Close = %v, then %v, want nil twice", errs[0], errs[1])
	}
	if !errors.Is(errs[2], ErrClosed) || !errors.Is(errs[3], ErrClosed) {
		t.Errorf("Read = %v, Write = %v, want ErrClosed", errs[2], errs[3])
	}
	if len(errs) > 4 {
		t.Error(errs[4])
	}
}

func TestCloseDropsMessagesAndTimesOut(t *testing.T) {
	old := closeTimeout
	closeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { closeTimeout = old })

	done := make(chan time.Duration, 1)
	srv := server(t, func(c *tctx) error {
		conn, err := Accept(c)
		if err != nil {
			return err
		}
		// Wait for the message, which the reader holds, then close.
		time.Sleep(50 * time.Millisecond)
		start := time.Now()
		_ = conn.Close(StatusNormalClosure, "")
		done <- time.Since(start)
		return nil
	})
	c := dial(t, srv)
	c.send(opText, []byte("unread"))
	c.send(opText, []byte("unread too"))
	c.expectClose(StatusNormalClosure)
	// The client never answers the close.
	c.expectEOF()
	if d := <-done; d < 50*time.Millisecond {
		t.Errorf("Close took %v, want the close timeout", d)
	}
}

func TestCloseArguments(t *testing.T) {
	results := make(chan []error, 1)
	srv := server(t, func(c *tctx) error {
		conn, err := Accept(c)
		if err != nil {
			return err
		}
		results <- []error{
			conn.Close(StatusNoStatusReceived, ""),
			conn.Close(StatusNormalClosure, strings.Repeat("x", 124)),
			conn.Close(StatusNormalClosure, "\xFF"),
		}
		return nil
	})
	dial(t, srv)
	for i, err := range <-results {
		if err == nil {
			t.Errorf("Close %d = nil, want an error", i)
		}
	}
}

func TestReadContext(t *testing.T) {
	results := make(chan string, 2)
	srv := server(t, func(c *tctx) error {
		conn, err := Accept(c)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close(StatusNormalClosure, "") }()
		ctx, cancel := context.WithTimeout(c, 20*time.Millisecond)
		defer cancel()
		_, _, err = conn.Read(ctx)
		results <- err.Error()
		if err := conn.Write(c, Text, []byte("go")); err != nil {
			return err
		}
		_, msg, err := conn.Read(c)
		if err != nil {
			return err
		}
		results <- string(msg)
		return nil
	})
	c := dial(t, srv)
	if f := c.mustRecv(); string(f.p) != "go" {
		t.Fatalf("frame = %q", f.p)
	}
	c.send(opText, []byte("after"))
	if got := <-results; got != context.DeadlineExceeded.Error() {
		t.Errorf("first Read = %q, want the deadline", got)
	}
	if got := <-results; got != "after" {
		t.Errorf("second Read = %q, want the message", got)
	}
}

func TestWriteErrors(t *testing.T) {
	results := make(chan []error, 1)
	srv := server(t, func(c *tctx) error {
		conn, err := Accept(c)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close(StatusNormalClosure, "") }()
		canceled, cancel := context.WithCancel(c)
		cancel()
		results <- []error{
			conn.Write(c, Text, []byte{0xFF}),
			conn.Write(c, MessageType(9), nil),
			conn.WriteJSON(c, make(chan int)),
			conn.Write(canceled, Text, []byte("x")),
			conn.WriteJSON(c, map[string]int{"a": 1}),
		}
		return nil
	})
	c := dial(t, srv)
	errs := <-results
	for i, err := range errs[:4] {
		if err == nil {
			t.Errorf("write %d = nil, want an error", i)
		}
	}
	if errs[4] != nil {
		t.Fatalf("WriteJSON = %v", errs[4])
	}
	if f := c.mustRecv(); f.op != opText || string(f.p) != `{"a":1}` {
		t.Errorf("frame = %#x %q, want the JSON", f.op, f.p)
	}
}

func TestWriteTimeoutDropsTheConnection(t *testing.T) {
	results := make(chan error, 2)
	run := func(opts []Option, ctx func(context.Context) (context.Context, context.CancelFunc)) router.HandlerFunc[*tctx] {
		return func(c *tctx) error {
			conn, err := Accept(c, opts...)
			if err != nil {
				return err
			}
			wctx, cancel := ctx(c)
			defer cancel()
			big := make([]byte, 1<<20)
			for {
				if err := conn.Write(wctx, Binary, big); err != nil {
					results <- err
					results <- conn.Write(c, Binary, nil)
					return nil
				}
			}
		}
	}
	plain := func(ctx context.Context) (context.Context, context.CancelFunc) { return ctx, func() {} }
	timeout := func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, 100*time.Millisecond)
	}
	cancelLater := func(ctx context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(ctx)
		time.AfterFunc(100*time.Millisecond, cancel)
		return ctx, cancel
	}
	for _, tc := range []struct {
		ctx  func(context.Context) (context.Context, context.CancelFunc)
		name string
		opts []Option
	}{
		{name: "WriteTimeout", opts: []Option{WriteTimeout(100 * time.Millisecond)}, ctx: plain},
		{name: "context deadline", opts: []Option{WriteTimeout(-1)}, ctx: timeout},
		{name: "canceled context", ctx: cancelLater},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := server(t, run(tc.opts, tc.ctx))
			dial(t, srv) // which never reads
			first, second := <-results, <-results
			if first == nil || second == nil {
				t.Errorf("writes = %v, %v, want both to fail", first, second)
			}
		})
	}
}

func TestConcurrentWrites(t *testing.T) {
	const writers, each = 8, 50
	srv := server(t, func(c *tctx) error {
		conn, err := Accept(c)
		if err != nil {
			return err
		}
		var wg sync.WaitGroup
		for i := range writers {
			wg.Go(func() {
				msg := bytes.Repeat([]byte{byte('a' + i)}, 1000+i)
				for range each {
					_ = conn.Write(c, Binary, msg)
				}
			})
		}
		wg.Wait()
		return conn.Close(StatusNormalClosure, "")
	})
	c := dial(t, srv)
	for range writers * each {
		f := c.mustRecv()
		i := int(f.p[0] - 'a')
		if f.op != opBinary || !bytes.Equal(f.p, bytes.Repeat([]byte{f.p[0]}, 1000+i)) {
			t.Fatalf("frame %#x of %d bytes is torn", f.op, len(f.p))
		}
	}
	c.expectClose(StatusNormalClosure)
}

func TestHeartbeat(t *testing.T) {
	t.Run("answered", func(t *testing.T) {
		srv := server(t, echo(nil, Heartbeat(20*time.Millisecond)))
		c := dial(t, srv)
		for range 5 {
			f := c.mustRecv()
			if f.op != opPing {
				t.Fatalf("frame %#x, want a ping", f.op)
			}
			c.send(opPong, f.p)
		}
		c.send(opText, []byte("alive"))
		for {
			if f := c.mustRecv(); f.op == opText {
				break
			}
		}
	})
	t.Run("unanswered", func(t *testing.T) {
		errs := make(chan error, 1)
		srv := server(t, echo(errs, Heartbeat(20*time.Millisecond)))
		c := dial(t, srv)
		if f := c.mustRecv(); f.op != opPing {
			t.Fatalf("frame %#x, want a ping", f.op)
		}
		c.expectEOF()
		if err := <-errs; !errors.Is(err, ErrHeartbeatTimeout) {
			t.Errorf("Read = %v, want ErrHeartbeatTimeout", err)
		}
	})
}

func TestReadTimeout(t *testing.T) {
	errs := make(chan error, 1)
	srv := server(t, echo(errs, ReadTimeout(50*time.Millisecond)))
	c := dial(t, srv)
	c.send(opText, []byte("in time"))
	c.mustRecv()
	c.expectEOF()
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Read = %v, want a timeout", err)
	}
}

func TestDrainSendsServiceRestart(t *testing.T) {
	draining := make(chan struct{})
	errs := make(chan error, 1)
	srv := server(t, echo(errs), func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(drain.With(r.Context(), draining)))
		})
	})
	c := dial(t, srv)
	c.send(opText, []byte("before"))
	c.mustRecv()
	close(draining)
	if reason := c.expectClose(StatusServiceRestart); reason != "service restart" {
		t.Errorf("reason = %q", reason)
	}
	c.send(opClose, closePayload(StatusServiceRestart, ""))
	c.expectEOF()
	if err := <-errs; !errors.Is(err, ErrClosed) {
		t.Errorf("Read = %v, want ErrClosed", err)
	}
}

func TestNetworkErrorEndsRead(t *testing.T) {
	errs := make(chan error, 1)
	srv := server(t, echo(errs))
	c := dial(t, srv)
	c.write([]byte{finBit | opText}) // half a header
	_ = c.conn.Close()
	if err := <-errs; err == nil {
		t.Error("Read = nil, want the network error")
	}
}

func TestStrings(t *testing.T) {
	if Text.String() != "text" || Binary.String() != "binary" || MessageType(7).String() != "MessageType(7)" {
		t.Error("MessageType.String")
	}
	if got := (&CloseError{Code: 1000}).Error(); got != "ws: the client closed the connection with code 1000" {
		t.Errorf("Error = %q", got)
	}
	if CloseStatus(errors.New("x")) != -1 {
		t.Error("CloseStatus of a plain error is not -1")
	}
	if got := violation(StatusProtocolError, "x %d", 1).Error(); got != "ws: x 1" {
		t.Errorf("protocolError = %q", got)
	}
}

func TestStatusCodeValid(t *testing.T) {
	for _, c := range []StatusCode{1000, 1003, 1007, 1014, 3000, 4999} {
		if !c.valid() {
			t.Errorf("%d is not valid", c)
		}
	}
	for _, c := range []StatusCode{0, 999, 1004, 1005, 1006, 1015, 1016, 2999, 5000} {
		if c.valid() {
			t.Errorf("%d is valid", c)
		}
	}
}

func TestFramesSentWithTheHandshake(t *testing.T) {
	srv := server(t, echo(nil))
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/ws", nil)
	req.Header = upgradeHeader()
	var buf bytes.Buffer
	_ = req.Write(&buf)
	buf.Write(frame(true, opText, []byte("early")))
	if _, err := conn.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, conn: conn, br: bufio.NewReader(conn)}
	//nolint:bodyclose // A 101 carries no body; the deferred Close ends the connection.
	if c.resp, err = http.ReadResponse(c.br, req); err != nil || c.resp.StatusCode != 101 {
		t.Fatalf("handshake = %v, %v", c.resp, err)
	}
	if f := c.mustRecv(); string(f.p) != "early" {
		t.Errorf("frame = %q, want the frame sent with the handshake", f.p)
	}
}
