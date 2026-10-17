package ja4

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

// maxRecord is the largest TLS record body the capture buffers: the largest
// payload the protocol allows, and so the bound of a ClientHello that fits one
// record. A longer declared length is never read into memory here; the TLS
// handshake reads the record itself and refuses it.
const maxRecord = 16384

// Listener wraps ln so that each connection it accepts captures the JA4 of
// its ClientHello.
func Listener(ln net.Listener) net.Listener { return listener{Listener: ln} }

type listener struct{ net.Listener }

func (l listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &Conn{Conn: c}, nil
}

// Conn reads the first TLS record of the connection on its first Read, before
// the TLS handshake takes it, computes the JA4, and then hands the same bytes
// to the handshake. A record that does not parse leaves the JA4 empty and the
// connection as it was. The read keeps the deadline of the server and sets
// none of its own.
type Conn struct {
	net.Conn
	prefix *bytes.Reader
	ja4    atomic.Pointer[string]
	once   sync.Once
}

// Read replays the captured record, then reads the connection.
func (c *Conn) Read(p []byte) (int, error) {
	c.once.Do(c.peek)
	if c.prefix.Len() > 0 {
		return c.prefix.Read(p)
	}
	return c.Conn.Read(p)
}

func (c *Conn) peek() {
	header := make([]byte, 5)
	n, err := io.ReadFull(c.Conn, header)
	if err != nil {
		// Replay what arrived, so a short read drops no byte from the stream.
		c.prefix = bytes.NewReader(header[:n])
		return
	}
	size := int(header[3])<<8 | int(header[4])
	if size > maxRecord {
		c.prefix = bytes.NewReader(header)
		return
	}
	record := make([]byte, 5+size)
	copy(record, header)
	n, _ = io.ReadFull(c.Conn, record[5:])
	record = record[:5+n]
	c.prefix = bytes.NewReader(record)
	if s, ok := FromRecord(record); ok {
		c.ja4.Store(&s)
	}
}

// JA4 reports the captured fingerprint, or "" before the handshake and for a
// ClientHello that did not parse.
func (c *Conn) JA4() string {
	if s := c.ja4.Load(); s != nil {
		return *s
	}
	return ""
}

// connOf recovers the Conn of an accepted connection, under the *tls.Conn
// that http.Server.ServeTLS wraps around it.
func connOf(c net.Conn) *Conn {
	switch v := c.(type) {
	case *Conn:
		return v
	case *tls.Conn:
		if fc, ok := v.NetConn().(*Conn); ok {
			return fc
		}
	}
	return nil
}

type (
	connKey    struct{}
	vouchedKey struct{}
)

// vouched is what a trusted CDN reported in its headers.
type vouched struct{ ja4, ja3 string }

// WithConn returns ctx carrying the Conn under c, for http.Server.ConnContext.
// A connection that Listener did not accept leaves ctx as it is.
func WithConn(ctx context.Context, c net.Conn) context.Context {
	if fc := connOf(c); fc != nil {
		return context.WithValue(ctx, connKey{}, fc)
	}
	return ctx
}

// WithVouched returns ctx carrying the JA4 and the JA3 that a trusted CDN
// reported, either of which may be empty.
func WithVouched(ctx context.Context, ja4, ja3 string) context.Context {
	return context.WithValue(ctx, vouchedKey{}, vouched{ja4: ja4, ja3: ja3})
}

// JA4 reports the JA4 a trusted CDN vouched for, or else the one the
// connection captured, or "".
func JA4(ctx context.Context) string {
	if v, ok := ctx.Value(vouchedKey{}).(vouched); ok && v.ja4 != "" {
		return v.ja4
	}
	if c, ok := ctx.Value(connKey{}).(*Conn); ok {
		return c.JA4()
	}
	return ""
}

// JA3 reports the JA3 a trusted CDN vouched for, or "".
func JA3(ctx context.Context) string {
	v, _ := ctx.Value(vouchedKey{}).(vouched)
	return v.ja3
}
