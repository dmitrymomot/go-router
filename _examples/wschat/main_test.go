package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newRouter(newRoom()))
	t.Cleanup(srv.Close)
	return srv
}

func TestAssets(t *testing.T) {
	srv := newServer(t)
	for path, want := range map[string]string{
		"/":          "text/html; charset=utf-8",
		"/app.js":    "text/javascript; charset=utf-8",
		"/style.css": "text/css; charset=utf-8",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != want {
			t.Errorf("%s: %d %q, want 200 %q", path, resp.StatusCode, resp.Header.Get("Content-Type"), want)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: CSP = %q", path, csp)
		}
	}
}

// client is the least of a WebSocket client: a handshake, masked text frames
// out, unmasked frames in.
type client struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

func dial(t *testing.T, srv *httptest.Server, query, origin string) (*client, int, http.Header) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	key := make([]byte, 16)
	_, _ = rand.Read(key)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/ws?"+query, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString(key))
	req.Header.Set("Sec-WebSocket-Protocol", "chat.v1")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatal(err)
	}
	// A 101 has no body: the frames follow on the connection.
	_ = resp.Body.Close()
	return &client{t: t, conn: conn, br: br}, resp.StatusCode, resp.Header
}

func join(t *testing.T, srv *httptest.Server, name string) *client {
	t.Helper()
	c, status, header := dial(t, srv, "name="+name, srv.URL)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("join %s: status %d, want 101", name, status)
	}
	if got := header.Get("Sec-WebSocket-Protocol"); got != "chat.v1" {
		t.Errorf("subprotocol = %q, want chat.v1", got)
	}
	return c
}

func (c *client) send(op byte, p []byte) {
	c.t.Helper()
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	b := []byte{0x80 | op, 0x80 | byte(len(p))} // the tests send less than 126 bytes
	b = append(b, mask[:]...)
	for i, v := range p {
		b = append(b, v^mask[i&3])
	}
	if _, err := c.conn.Write(b); err != nil {
		c.t.Fatal(err)
	}
}

// frame reads one frame and reports its opcode and payload.
func (c *client) frame() (byte, []byte) {
	c.t.Helper()
	var h [8]byte
	if _, err := io.ReadFull(c.br, h[:2]); err != nil {
		c.t.Fatal(err)
	}
	n := int(h[1] & 0x7F)
	if n == 126 {
		_, _ = io.ReadFull(c.br, h[:2])
		n = int(binary.BigEndian.Uint16(h[:2]))
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(c.br, p); err != nil {
		c.t.Fatal(err)
	}
	return h[0] & 0x0F, p
}

// next reads frames until a chat message, answering pings on the way.
func (c *client) next() message {
	c.t.Helper()
	for {
		op, p := c.frame()
		switch op {
		case 0x1:
			var m message
			if err := json.Unmarshal(p, &m); err != nil {
				c.t.Fatalf("message %q: %v", p, err)
			}
			return m
		case 0x9:
			c.send(0xA, p)
		case 0x8:
			c.t.Fatalf("the server closed with %v", p)
		}
	}
}

func TestChat(t *testing.T) {
	srv := newServer(t)
	ann := join(t, srv, "Ann")
	if m := ann.next(); m.Kind != kindNotice || m.Author != "Ann" {
		t.Fatalf("first message = %+v, want the notice of Ann", m)
	}
	bob := join(t, srv, "%20Bob%0A")
	if m := ann.next(); m.Kind != kindNotice || m.Author != "Bob" || m.Text != "joined the chat" {
		t.Fatalf("Ann sees %+v, want Bob join", m)
	}
	bob.next() // his own notice

	ann.send(0x1, []byte(`{"text":"  hi\u0007   there "}`))
	for _, c := range []*client{ann, bob} {
		if m := c.next(); m.Kind != kindMessage || m.Author != "Ann" || m.Text != "hi there" {
			t.Errorf("got %+v, want the cleaned message of Ann", m)
		}
	}

	bob.send(0x8, []byte{0x03, 0xE8}) // a close with 1000
	if m := ann.next(); m.Author != "Bob" || m.Text != "left the chat" {
		t.Errorf("Ann sees %+v, want Bob leave", m)
	}
}

func TestBinaryClosesWith1003(t *testing.T) {
	srv := newServer(t)
	c := join(t, srv, "Ann")
	c.next()
	c.send(0x2, []byte{1, 2, 3})
	for {
		op, p := c.frame()
		if op != 0x8 {
			continue
		}
		if code := binary.BigEndian.Uint16(p); code != 1003 {
			t.Errorf("close code = %d, want 1003", code)
		}
		return
	}
}

func TestRefusals(t *testing.T) {
	srv := newServer(t)
	if _, status, _ := dial(t, srv, "name=%20", srv.URL); status != http.StatusBadRequest {
		t.Errorf("a blank name: status %d, want 400", status)
	}
	if _, status, _ := dial(t, srv, "name=Eve", "https://evil.test"); status != http.StatusForbidden {
		t.Errorf("a foreign origin: status %d, want 403", status)
	}
}

func TestClean(t *testing.T) {
	if got := cleanName(strings.Repeat("é", 30)); got != strings.Repeat("é", maxNameRunes) {
		t.Errorf("cleanName = %q", got)
	}
}
