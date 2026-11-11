// Package ws serves WebSocket connections from go-router, the server side of
// RFC 6455, with the standard library alone.
//
// [Accept] answers the opening handshake and returns a [Conn], which reads and
// writes whole messages:
//
//	r.GET("/echo", func(c *Context) error {
//		conn, err := ws.Accept(c, ws.Heartbeat(30*time.Second))
//		if err != nil {
//			return err
//		}
//		defer conn.Close(ws.StatusNormalClosure, "")
//		for {
//			typ, msg, err := conn.Read(c)
//			if err != nil {
//				return nil
//			}
//			if err := conn.Write(c, typ, msg); err != nil {
//				return nil
//			}
//		}
//	})
//
// The connection answers pings, joins fragmented messages, checks that text is
// valid UTF-8 and closes with the right code when a client breaks a rule. When
// the drain of serve.Run starts, it sends every open connection a close frame
// with [StatusServiceRestart], as package sse ends its streams. It does not
// negotiate permessage-deflate or any other extension.
package ws

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // RFC 6455 defines the accept key with SHA-1.
	"encoding/base64"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/internal/drain"
	"github.com/dmitrymomot/go-router/internal/routerhook"
)

// The headers of the handshake, besides those of package router.
const (
	HeaderUpgrade              = "Upgrade"
	HeaderSecWebSocketKey      = "Sec-WebSocket-Key"
	HeaderSecWebSocketAccept   = "Sec-WebSocket-Accept"
	HeaderSecWebSocketVersion  = "Sec-WebSocket-Version"
	HeaderSecWebSocketProtocol = "Sec-WebSocket-Protocol"
)

// MessageType is the type of a data message.
type MessageType int

// The two types of data message.
const (
	Text   MessageType = opText
	Binary MessageType = opBinary
)

func (t MessageType) String() string {
	switch t {
	case Text:
		return "text"
	case Binary:
		return "binary"
	}
	return "MessageType(" + strconv.Itoa(int(t)) + ")"
}

// StatusCode is the code of a close frame, from RFC 6455, section 7.4, and
// the IANA registry.
type StatusCode int

// The close codes. [StatusNoStatusReceived] and [StatusAbnormalClosure] never
// go on the wire: the first reports a close frame with no code, the second a
// connection that ended with no close frame.
const (
	StatusNormalClosure      StatusCode = 1000
	StatusGoingAway          StatusCode = 1001
	StatusProtocolError      StatusCode = 1002
	StatusUnsupportedData    StatusCode = 1003
	StatusNoStatusReceived   StatusCode = 1005
	StatusAbnormalClosure    StatusCode = 1006
	StatusInvalidPayload     StatusCode = 1007
	StatusPolicyViolation    StatusCode = 1008
	StatusMessageTooBig      StatusCode = 1009
	StatusMandatoryExtension StatusCode = 1010
	StatusInternalError      StatusCode = 1011
	StatusServiceRestart     StatusCode = 1012
	StatusTryAgainLater      StatusCode = 1013
	StatusBadGateway         StatusCode = 1014
)

// valid reports whether c may travel in a close frame: a defined code below
// 1015 other than 1004, 1005 and 1006, or a code of the ranges 3000-3999 for
// libraries and 4000-4999 for private use.
func (c StatusCode) valid() bool {
	switch {
	case c >= 1000 && c <= 1003, c >= 1007 && c <= 1014:
		return true
	default:
		return c >= 3000 && c <= 4999
	}
}

// CloseError is what [Conn.Read] reports once the client closed the
// connection, with the code and the reason of its close frame. Code is
// [StatusNoStatusReceived] for a close frame that carried no code.
type CloseError struct {
	Reason string
	Code   StatusCode
}

func (e *CloseError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("ws: the client closed the connection with code %d", e.Code)
	}
	return fmt.Sprintf("ws: the client closed the connection with code %d: %s", e.Code, e.Reason)
}

// CloseStatus reports the code of the close frame behind err, or -1 when err
// holds no [*CloseError].
func CloseStatus(err error) StatusCode {
	if ce, ok := errors.AsType[*CloseError](err); ok {
		return ce.Code
	}
	return -1
}

var (
	// ErrClosed is what a [Conn] reports once [Conn.Close] ran, the handler
	// returned or the drain of serve.Run closed it.
	ErrClosed = errors.New("ws: the connection is closed")

	// ErrHeartbeatTimeout is what a [Conn] reports once a ping of [Heartbeat]
	// got no pong in time.
	ErrHeartbeatTimeout = errors.New("ws: the client answered no ping in time")
)

// Defaults of the options.
const (
	// DefaultReadLimit is the largest message a connection reads when no
	// [ReadLimit] option says otherwise.
	DefaultReadLimit = 32 << 10

	// DefaultWriteTimeout bounds each write to the client when no
	// [WriteTimeout] option says otherwise.
	DefaultWriteTimeout = 30 * time.Second

	// CloseTimeout is how long [Conn.Close] waits for the close frame of the
	// client before it drops the connection.
	CloseTimeout = 5 * time.Second
)

type config struct {
	subprotocols []string
	origins      []string
	readLimit    int64
	readTimeout  time.Duration
	writeTimeout time.Duration
	heartbeat    time.Duration
}

// Option configures a connection. See [Subprotocols], [Origins], [ReadLimit],
// [ReadTimeout], [WriteTimeout] and [Heartbeat].
type Option func(*config)

// Subprotocols lists the subprotocols the server speaks, the one it likes
// best first. The handshake picks the first of them that the client offers,
// and [Conn.Subprotocol] reports it. A client that offers none of them gets
// no subprotocol, and may close the connection.
func Subprotocols(protocols ...string) Option {
	return func(c *config) { c.subprotocols = append(c.subprotocols, protocols...) }
}

// Origins lists the origins, besides the host of the request, whose pages may
// open a connection. A browser sends the Origin header with every handshake,
// and a page of any site can open a WebSocket, so the check is what keeps a
// foreign page from using the cookies of a visitor.
//
// Each entry is an origin such as "https://app.example.com", matched without
// regard to case, and may hold the wildcards of [path.Match], as in
// "https://*.example.com". The single entry "*" lets every origin in. A
// request without the Origin header, which is what a client outside a
// browser sends, always passes.
func Origins(patterns ...string) Option {
	return func(c *config) { c.origins = append(c.origins, patterns...) }
}

// ReadLimit sets the largest message, in bytes, that the connection reads. A
// client that sends a larger one gets a close frame with
// [StatusMessageTooBig]. Zero or less takes [DefaultReadLimit].
func ReadLimit(n int64) Option {
	return func(c *config) { c.readLimit = n }
}

// ReadTimeout drops a connection on which no frame arrives within d, a ping
// or a pong included. Zero, the default, sets no limit; [Heartbeat] is the
// way to find a client that is gone, and ReadTimeout the way to bound one
// that sends a frame slowly.
func ReadTimeout(d time.Duration) Option {
	return func(c *config) { c.readTimeout = d }
}

// WriteTimeout bounds each write to the client, so a client that stops
// reading cannot hold a writer forever. A write that times out drops the
// connection. Zero takes [DefaultWriteTimeout], and a negative d sets no
// deadline at all.
func WriteTimeout(d time.Duration) Option {
	return func(c *config) { c.writeTimeout = d }
}

// Heartbeat sends a ping every d and drops the connection when the client
// sends no pong before the next one is due. [Conn.Read] then reports
// [ErrHeartbeatTimeout].
func Heartbeat(d time.Duration) Option {
	return func(c *config) { c.heartbeat = d }
}

// closeTimeout is CloseTimeout, which a test shortens.
var closeTimeout = CloseTimeout

// acceptGUID is the key of RFC 6455, section 1.3.
const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + acceptGUID)) //nolint:gosec // See the import.
	return base64.StdEncoding.EncodeToString(h[:])
}

// Accept answers the WebSocket handshake of c and takes the connection. It
// reports a [router.HTTPError] for a request it refuses, and writes nothing,
// so the error handler answers it:
//
//   - 405 for a method other than GET;
//   - 426 with "Upgrade: websocket" for a request that asks no upgrade;
//   - 426 with "Sec-WebSocket-Version: 13" for another version;
//   - 400 for a missing or malformed Sec-WebSocket-Key;
//   - 403 for an Origin that [Origins] does not allow;
//   - 500 for a nil option, a response that went out already, a writer that
//     cannot be hijacked or an HTTP/2 request, which this package does not
//     upgrade.
//
// The headers that the handler and the middleware set on the response, such
// as a cookie, go out with the answer.
//
// After Accept the connection belongs to the [Conn], which a router closes
// when the handler returns, so call [Conn.Close] before then to choose the
// code. A Conn that no router serves must be closed by hand.
func Accept(c router.Context, opts ...Option) (*Conn, error) {
	var cfg config
	for _, opt := range opts {
		if opt == nil {
			return nil, router.ErrInternalServerError.WithError(errors.New("ws: an option cannot be nil"))
		}
		opt(&cfg)
	}
	b := baseOf(c)
	req, res := b.Request(), b.Response()
	h := res.Header()

	if req.Method != http.MethodGet {
		h.Set(router.HeaderAllow, http.MethodGet)
		return nil, router.ErrMethodNotAllowed
	}
	if !b.IsWebSocket() {
		h.Set(router.HeaderConnection, "Upgrade")
		h.Set(HeaderUpgrade, "websocket")
		return nil, router.NewHTTPError(http.StatusUpgradeRequired, "")
	}
	if req.Header.Get(HeaderSecWebSocketVersion) != "13" {
		h.Set(HeaderSecWebSocketVersion, "13")
		return nil, router.NewHTTPError(http.StatusUpgradeRequired, "").
			WithMessage("Unsupported WebSocket version")
	}
	key := strings.TrimSpace(req.Header.Get(HeaderSecWebSocketKey))
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
		return nil, router.ErrBadRequest.WithMessage("Invalid Sec-WebSocket-Key")
	}
	if !originAllowed(req, cfg.origins) {
		return nil, router.ErrForbidden.WithMessage("Origin not allowed")
	}
	if req.ProtoMajor != 1 {
		return nil, router.ErrInternalServerError.WithError(
			fmt.Errorf("ws: an HTTP/%d.%d request cannot be upgraded", req.ProtoMajor, req.ProtoMinor))
	}
	if res.Committed {
		return nil, router.ErrInternalServerError.WithError(errors.New("ws: the response went out before the handshake"))
	}
	protocol := pickSubprotocol(req, cfg.subprotocols)

	netConn, brw, err := res.Hijack()
	if err != nil {
		return nil, router.ErrInternalServerError.WithError(fmt.Errorf("ws: take the connection: %w", err))
	}
	conn := newConn(b, netConn, brw, cfg, protocol)
	if err := conn.handshake(h, acceptKey(key)); err != nil {
		_ = netConn.Close()
		// The connection is gone, so the error handler has nothing to answer.
		return nil, err
	}
	conn.start()
	return conn, nil
}

// baseOf reports the Base of c, or a Base over its request and response when
// c hides it.
func baseOf(c router.Context) *router.Base {
	if b, ok := router.FromContext(c); ok {
		return b
	}
	return router.NewBase(c.Response(), c.Request())
}

func originAllowed(req *http.Request, patterns []string) bool {
	origin := req.Header.Get(router.HeaderOrigin)
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, req.Host) {
		return true
	}
	origin = strings.ToLower(origin)
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if ok, _ := path.Match(strings.ToLower(p), origin); ok {
			return true
		}
	}
	return false
}

func pickSubprotocol(req *http.Request, supported []string) string {
	if len(supported) == 0 {
		return ""
	}
	var offered []string
	for _, v := range req.Header.Values(HeaderSecWebSocketProtocol) {
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				offered = append(offered, p)
			}
		}
	}
	for _, s := range supported {
		if slices.Contains(offered, s) {
			return s
		}
	}
	return ""
}

// The headers of the response that the handshake writes itself, or that make
// no sense on a 101.
var handshakeHeaders = []string{
	router.HeaderConnection,
	HeaderUpgrade,
	router.HeaderContentLength,
	router.HeaderContentType,
	"Transfer-Encoding",
	HeaderSecWebSocketAccept,
	HeaderSecWebSocketProtocol,
	HeaderSecWebSocketVersion,
	"Sec-WebSocket-Extensions",
}

func (c *Conn) handshake(h http.Header, accept string) error {
	extra := h.Clone()
	for _, name := range handshakeHeaders {
		extra.Del(name)
	}
	w := c.bw
	_, _ = w.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n")
	_, _ = w.WriteString(HeaderSecWebSocketAccept + ": " + accept + "\r\n")
	if c.protocol != "" {
		_, _ = w.WriteString(HeaderSecWebSocketProtocol + ": " + c.protocol + "\r\n")
	}
	_ = extra.Write(w) // A bufio.Writer reports the error of the flush.
	_, _ = w.WriteString("\r\n")
	if err := c.armWrite(context.Background()); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("ws: write the handshake: %w", err)
	}
	return nil
}

// message is one data message on its way from the reader to [Conn.Read].
type message struct {
	data []byte
	typ  MessageType
}

// Conn is a WebSocket connection that [Accept] opened.
//
// Writes are safe for concurrent use: [Conn.Write], [Conn.WriteJSON],
// [Conn.Close] and the pongs and pings of the connection itself take turns,
// and each sends a whole frame. [Conn.Read] may run on one goroutine at a
// time, while others write.
//
// A goroutine reads the wire from Accept on, so a ping gets its pong and a
// close frame its answer even while the handler writes. It holds one message
// until [Conn.Read] takes it, and reads nothing more meanwhile, so a handler
// that stops reading also stops answering pings. Call Read in a loop for as
// long as the connection is open.
type Conn struct {
	b        *router.Base
	netConn  net.Conn
	br       *bufio.Reader
	bw       *bufio.Writer
	protocol string
	cfg      config

	// wmu is the write lock: a channel, so a writer can give up waiting for
	// it when its context ends.
	wmu  chan struct{}
	wbuf []byte
	// werr is the first write failure, under wmu. sentClose is set once a
	// close frame went out, under wmu.
	werr      error
	sentClose bool

	msgs     chan message
	readDone chan struct{} // closed when the reader stops; rerr is then set
	rerr     error

	closing     chan struct{} // closed when the connection starts to close
	closingOnce sync.Once
	// localErr is what Read reports once the server ended the connection:
	// ErrClosed, or ErrHeartbeatTimeout.
	localErr atomic.Pointer[error]

	done  chan struct{} // closed when every goroutine of the connection returned
	watch chan struct{} // closed when the watch goroutine returned

	pongs atomic.Int64 // the pongs that arrived, for the heartbeat
}

// ownReader reads what the server buffered past the handshake, then netConn.
// The reader of the hijack would do the same, but a failed read through it
// cancels the context of the request, which the handler still uses.
func ownReader(netConn net.Conn, buffered *bufio.Reader) *bufio.Reader {
	var r io.Reader = netConn
	if n := buffered.Buffered(); n > 0 {
		p, _ := buffered.Peek(n) // Peek of what is buffered cannot fail.
		r = io.MultiReader(bytes.NewReader(bytes.Clone(p)), netConn)
	}
	return bufio.NewReader(r)
}

func newConn(b *router.Base, netConn net.Conn, brw *bufio.ReadWriter, cfg config, protocol string) *Conn {
	if cfg.readLimit <= 0 {
		cfg.readLimit = DefaultReadLimit
	}
	if cfg.writeTimeout == 0 {
		cfg.writeTimeout = DefaultWriteTimeout
	}
	// The server set deadlines on the connection for the request. They no
	// longer apply: the options set their own.
	_ = netConn.SetDeadline(time.Time{})
	return &Conn{
		b:        b,
		netConn:  netConn,
		br:       ownReader(netConn, brw.Reader),
		bw:       bufio.NewWriter(netConn),
		protocol: protocol,
		cfg:      cfg,
		wmu:      make(chan struct{}, 1),
		msgs:     make(chan message),
		readDone: make(chan struct{}),
		closing:  make(chan struct{}),
		done:     make(chan struct{}),
		watch:    make(chan struct{}),
	}
}

func (c *Conn) start() {
	go c.read()
	go c.watchLoop(drain.Done(c.b.Request().Context()))
	go func() {
		<-c.readDone
		<-c.watch
		close(c.done)
	}()
	// The connection must not outlive the handler: net/http forgets a
	// hijacked connection, so nothing else would close it.
	routerhook.OnHandlerDone(c.b, c.release)
}

// Subprotocol reports the subprotocol that the handshake picked, or "".
func (c *Conn) Subprotocol() string { return c.protocol }

// Request reports the request of the handshake.
func (c *Conn) Request() *http.Request { return c.b.Request() }

// Done returns a channel that closes once the connection is closed and every
// goroutine of it has returned.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Read waits for the next data message and reports its type and payload. A
// text message is valid UTF-8. The payload belongs to the caller.
//
// When ctx ends first, Read reports its error and leaves the connection open,
// and the next Read gets the message. Once the connection closed, Read reports
// why: a [*CloseError] when the client closed it, [ErrClosed] when the server
// did, [ErrHeartbeatTimeout], or the failure of the network or of a frame
// that broke the rules, which the connection answered with a close frame.
func (c *Conn) Read(ctx context.Context) (MessageType, []byte, error) {
	select {
	case m := <-c.msgs:
		return m.typ, m.data, nil
	case <-c.readDone:
		return 0, nil, c.readErr()
	case <-ctx.Done():
		select {
		case <-c.readDone:
			return 0, nil, c.readErr()
		default:
			return 0, nil, ctx.Err()
		}
	}
}

func (c *Conn) readErr() error {
	if p := c.localErr.Load(); p != nil {
		return *p
	}
	return c.rerr
}

// read runs on its own goroutine for the life of the connection.
func (c *Conn) read() {
	defer close(c.readDone)
	r := &frameReader{
		br:     c.br,
		limit:  c.cfg.readLimit,
		onPing: c.pong,
		onPong: func([]byte) { c.pongs.Add(1) },
	}
	if d := c.cfg.readTimeout; d > 0 {
		r.beforeFrame = func() { _ = c.netConn.SetReadDeadline(time.Now().Add(d)) }
	}
	for {
		typ, data, err := r.next()
		if err != nil {
			c.rerr = c.endRead(err)
			return
		}
		m := message{typ: typ, data: append([]byte(nil), data...)}
		r.release()
		select {
		case c.msgs <- m:
		case <-c.closing:
			// The server is closing: drop the message and read on until the
			// close frame of the client arrives.
		}
	}
}

// endRead answers the error that stopped the reader, closes the network
// connection and reports the error Read gives.
func (c *Conn) endRead(err error) error {
	defer c.shut()
	if pe, ok := errors.AsType[*protocolError](err); ok {
		_ = c.writeClose(context.Background(), pe.code, "")
		return err
	}
	if ce, ok := errors.AsType[*CloseError](err); ok {
		// Echo the code of the client, unless the server closed first.
		code := ce.Code
		if code == StatusNoStatusReceived {
			code = 0
		}
		_ = c.writeClose(context.Background(), code, "")
		return ce
	}
	return err
}

// shut marks the connection as closing and closes the network connection,
// which ends the reader and every write. The server closes the TCP connection
// first, as RFC 6455, section 7.1.1, asks.
func (c *Conn) shut() {
	c.markClosing()
	_ = c.netConn.Close()
}

func (c *Conn) markClosing() {
	c.closingOnce.Do(func() { close(c.closing) })
}

// fail ends the connection on the side of the server with err, which Read
// reports from then on, unless an earlier failure set its own.
func (c *Conn) fail(err error) {
	c.localErr.CompareAndSwap(nil, &err)
	c.shut()
}

// pong answers a ping. A failed write ends the connection, so the reader goes
// on until it sees that.
func (c *Conn) pong(payload []byte) error {
	_ = c.writeFrame(context.Background(), opPong, payload)
	return nil
}

func (c *Conn) watchLoop(draining <-chan struct{}) {
	defer close(c.watch)
	var beat <-chan time.Time
	if c.cfg.heartbeat > 0 {
		t := time.NewTicker(c.cfg.heartbeat)
		defer t.Stop()
		beat = t.C
	}
	var seen int64
	waiting := false
	for {
		select {
		case <-c.closing:
			return
		case <-draining:
			draining = nil
			go func() { _ = c.Close(StatusServiceRestart, "service restart") }()
		case <-beat:
			if n := c.pongs.Load(); waiting && n == seen {
				c.fail(ErrHeartbeatTimeout)
				return
			}
			seen, waiting = c.pongs.Load(), true
			_ = c.writeFrame(context.Background(), opPing, nil)
		}
	}
}

// release closes a connection that the handler left open, with
// [StatusGoingAway], and waits for its goroutines. The router calls it when
// the handler returns.
func (c *Conn) release() {
	c.localErr.CompareAndSwap(nil, &ErrClosed)
	_ = c.writeClose(context.Background(), StatusGoingAway, "")
	c.shut()
	<-c.done
}

// Close sends a close frame with code and reason, waits up to [CloseTimeout]
// for the close frame of the client, and drops the connection. Code must be
// one a close frame may carry, such as [StatusNormalClosure], and reason at
// most 123 bytes of valid UTF-8.
//
// Every later Read and write reports [ErrClosed]. A Close after the
// connection closed does nothing and reports nil.
func (c *Conn) Close(code StatusCode, reason string) error {
	if !code.valid() {
		return fmt.Errorf("ws: the close code %d cannot go in a close frame", code)
	}
	if len(reason) > maxCloseReason {
		return fmt.Errorf("ws: a close reason of %d bytes exceeds %d", len(reason), maxCloseReason)
	}
	if !utf8.ValidString(reason) {
		return errors.New("ws: a close reason must be valid UTF-8")
	}
	c.localErr.CompareAndSwap(nil, &ErrClosed)
	c.markClosing()
	err := c.writeClose(context.Background(), code, reason)
	if errors.Is(err, ErrClosed) {
		err = nil
	}
	t := time.NewTimer(closeTimeout)
	defer t.Stop()
	select {
	case <-c.readDone:
	case <-t.C:
	}
	c.shut()
	<-c.done
	return err
}

// Write sends one message of type typ. A text message must be valid UTF-8.
// ctx bounds the wait for the write lock and the write itself, along with
// [WriteTimeout]; a write that ctx cuts short drops the connection, because
// part of a frame may have gone out.
func (c *Conn) Write(ctx context.Context, typ MessageType, data []byte) error {
	switch typ {
	case Text:
		if !utf8.Valid(data) {
			return errors.New("ws: a text message must be valid UTF-8")
		}
	case Binary:
	default:
		return fmt.Errorf("ws: cannot write a message of type %v", typ)
	}
	return c.writeFrame(ctx, byte(typ), data)
}

// WriteJSON sends v as JSON in a text message. opts win over the options of
// [router.Router.JSONOptions].
func (c *Conn) WriteJSON(ctx context.Context, v any, opts ...json.Options) error {
	data, err := json.Marshal(v, routerhook.JSONOptions(c.b, opts)...)
	if err != nil {
		return fmt.Errorf("ws: encode message: %w", err)
	}
	return c.writeFrame(ctx, opText, data)
}

func (c *Conn) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.wmu <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Conn) unlock() { <-c.wmu }

// writeClose sends a close frame, unless one went out. Code zero sends a close
// frame without a code.
func (c *Conn) writeClose(ctx context.Context, code StatusCode, reason string) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	if c.sentClose {
		return ErrClosed
	}
	var p []byte
	if code != 0 {
		p = binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(reason)), uint16(code)) //nolint:gosec // A valid code is below 5000.
		p = append(p, reason...)
	}
	err := c.writeLocked(ctx, opClose, p)
	c.sentClose = true
	return err
}

func (c *Conn) writeFrame(ctx context.Context, opcode byte, p []byte) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	if c.sentClose {
		if err := c.werr; err != nil {
			return err
		}
		return ErrClosed
	}
	return c.writeLocked(ctx, opcode, p)
}

// writeLocked writes one frame. The caller holds the write lock.
func (c *Conn) writeLocked(ctx context.Context, opcode byte, p []byte) error {
	if c.werr != nil {
		return c.werr
	}
	if err := c.armWrite(ctx); err != nil {
		return c.failWrite(err)
	}
	// A context that ends mid-write moves the deadline into the past, which
	// fails the write.
	stop := context.AfterFunc(ctx, func() { _ = c.netConn.SetWriteDeadline(time.Unix(1, 0)) })
	defer stop()

	w := c.bw
	c.wbuf = appendHeader(c.wbuf[:0], true, opcode, len(p))
	_, _ = w.Write(c.wbuf)
	_, _ = w.Write(p)
	if err := w.Flush(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("ws: %w: %w", ctx.Err(), err)
		}
		return c.failWrite(err)
	}
	return nil
}

// failWrite records the first write failure and drops the connection.
func (c *Conn) failWrite(err error) error {
	c.werr = err
	c.sentClose = true // nothing more may go out
	c.shut()
	return err
}

// armWrite sets the write deadline of the next write: WriteTimeout from now,
// or the deadline of ctx when it is earlier.
func (c *Conn) armWrite(ctx context.Context) error {
	var deadline time.Time
	if d := c.cfg.writeTimeout; d > 0 {
		deadline = time.Now().Add(d)
	}
	if d, ok := ctx.Deadline(); ok && (deadline.IsZero() || d.Before(deadline)) {
		deadline = d
	}
	if err := c.netConn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("ws: set the write deadline: %w", err)
	}
	return nil
}
