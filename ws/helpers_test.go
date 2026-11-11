package ws

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
)

type tctx struct{ router.Base }

func newRouter() *router.Router[*tctx] {
	return router.New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
}

// sampleKey and sampleAccept are the example of RFC 6455, section 1.3.
const (
	sampleKey    = "dGhlIHNhbXBsZSBub25jZQ=="
	sampleAccept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
)

// server serves h at /ws through a router, wrapped by wrap when it is set.
func server(t *testing.T, h router.HandlerFunc[*tctx], wrap ...func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	r := newRouter()
	r.GET("/ws", h)
	r.POST("/ws", h)
	var handler http.Handler = r
	for _, w := range wrap {
		handler = w(handler)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// echo answers every message with itself, until Read fails, and reports the
// error of the last Read on errs.
func echo(errs chan<- error, opts ...Option) router.HandlerFunc[*tctx] {
	return func(c *tctx) error {
		conn, err := Accept(c, opts...)
		if err != nil {
			return err
		}
		for {
			typ, msg, err := conn.Read(c)
			if err != nil {
				if errs != nil {
					errs <- err
				}
				return nil
			}
			if err := conn.Write(c, typ, msg); err != nil {
				if errs != nil {
					errs <- err
				}
				return nil
			}
		}
	}
}

type client struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
	resp *http.Response
}

func upgradeHeader() http.Header {
	h := http.Header{}
	h.Set("Connection", "Upgrade")
	h.Set("Upgrade", "websocket")
	h.Set(HeaderSecWebSocketKey, sampleKey)
	h.Set(HeaderSecWebSocketVersion, "13")
	return h
}

// handshake sends a handshake with h and returns the connection and the
// response. A response other than 101 leaves the connection without a frame
// reader.
func handshake(t *testing.T, srv *httptest.Server, h http.Header) *client {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/ws", nil)
	req.Header = h
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req) //nolint:bodyclose // The connection is closed by the cleanup.
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, conn: conn, br: br, resp: resp}
}

func dial(t *testing.T, srv *httptest.Server) *client {
	t.Helper()
	c := handshake(t, srv, upgradeHeader())
	if c.resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", c.resp.StatusCode)
	}
	return c
}

// frame builds a masked client frame.
func frame(fin bool, op byte, p []byte) []byte {
	return rawFrame(fin, 0, op, true, p)
}

func rawFrame(fin bool, rsv, op byte, masked bool, p []byte) []byte {
	b0 := op | rsv<<4
	if fin {
		b0 |= finBit
	}
	var m byte
	if masked {
		m = maskBit
	}
	b := []byte{b0}
	switch n := len(p); {
	case n <= 125:
		b = append(b, m|byte(n))
	case n <= 0xFFFF:
		b = binary.BigEndian.AppendUint16(append(b, m|126), uint16(n))
	default:
		b = binary.BigEndian.AppendUint64(append(b, m|127), uint64(n))
	}
	if !masked {
		return append(b, p...)
	}
	var key [4]byte
	_, _ = rand.Read(key[:])
	b = append(b, key[:]...)
	for i, c := range p {
		b = append(b, c^key[i&3])
	}
	return b
}

func (c *client) write(b ...[]byte) {
	c.t.Helper()
	for _, p := range b {
		if _, err := c.conn.Write(p); err != nil {
			c.t.Fatal(err)
		}
	}
}

func (c *client) send(op byte, p []byte) { c.t.Helper(); c.write(frame(true, op, p)) }

type rframe struct {
	p   []byte
	fin bool
	op  byte
}

// recv reads one server frame, which must be unmasked.
func (c *client) recv() (rframe, error) {
	var f rframe
	var b [8]byte
	if _, err := io.ReadFull(c.br, b[:2]); err != nil {
		return f, err
	}
	f.fin, f.op = b[0]&finBit != 0, b[0]&0x0F
	if b[1]&maskBit != 0 {
		return f, fmt.Errorf("server frame is masked")
	}
	n := int64(b[1] & 0x7F)
	switch n {
	case 126:
		if _, err := io.ReadFull(c.br, b[:2]); err != nil {
			return f, err
		}
		n = int64(binary.BigEndian.Uint16(b[:2]))
	case 127:
		if _, err := io.ReadFull(c.br, b[:8]); err != nil {
			return f, err
		}
		n = int64(binary.BigEndian.Uint64(b[:8]))
	}
	f.p = make([]byte, n)
	_, err := io.ReadFull(c.br, f.p)
	return f, err
}

func (c *client) mustRecv() rframe {
	c.t.Helper()
	f, err := c.recv()
	if err != nil {
		c.t.Fatalf("read a frame: %v", err)
	}
	return f
}

// expectClose reads frames until a close frame, and checks its code.
func (c *client) expectClose(code StatusCode) string {
	c.t.Helper()
	for {
		f := c.mustRecv()
		if f.op != opClose {
			continue
		}
		if code == 0 {
			if len(f.p) != 0 {
				c.t.Fatalf("close payload = %v, want none", f.p)
			}
			return ""
		}
		if len(f.p) < 2 {
			c.t.Fatalf("close payload = %v, want the code %d", f.p, code)
		}
		if got := StatusCode(binary.BigEndian.Uint16(f.p)); got != code {
			c.t.Fatalf("close code = %d (%q), want %d", got, f.p[2:], code)
		}
		return string(f.p[2:])
	}
}

// expectEOF checks that the server closed the TCP connection.
func (c *client) expectEOF() {
	c.t.Helper()
	for {
		f, err := c.recv()
		if err != nil {
			return
		}
		if f.op != opClose {
			c.t.Fatalf("frame %#x after the close, want the end of the connection", f.op)
		}
	}
}

func closePayload(code StatusCode, reason string) []byte {
	return append(binary.BigEndian.AppendUint16(nil, uint16(code)), reason...)
}
