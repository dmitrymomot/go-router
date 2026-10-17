package ja4

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

// The example of the JA4 spec of FoxIO: a Chrome ClientHello with 15 ciphers
// and 16 extensions, SNI and ALPN h2 among them.
const foxioJA4 = "t13d1516h2_8daaf6152771_e5627efa2ab1"

func foxioHello() []byte {
	ciphers := []uint16{0x1301, 0x1302, 0x1303, 0xc02b, 0xc02f, 0xc02c, 0xc030, 0xcca9, 0xcca8, 0xc013, 0xc014, 0x009c, 0x009d, 0x002f, 0x0035, 0x0a0a}
	be := binary.BigEndian
	var exts []byte
	ext := func(typ uint16, body []byte) {
		exts = be.AppendUint16(exts, typ)
		exts = be.AppendUint16(exts, uint16(len(body)))
		exts = append(exts, body...)
	}
	u16list := func(vs ...uint16) []byte {
		b := be.AppendUint16(nil, uint16(2*len(vs)))
		for _, v := range vs {
			b = be.AppendUint16(b, v)
		}
		return b
	}
	ext(0x1a1a, nil) // GREASE
	ext(0x0000, []byte{0, 14, 0, 0, 11, 'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm'})
	ext(0x0017, nil)
	ext(0xff01, []byte{0})
	ext(0x000a, u16list(0x001d, 0x0017))
	ext(0x000b, []byte{1, 0})
	ext(0x0023, nil)
	ext(0x0010, []byte{0, 12, 2, 'h', '2', 8, 'h', 't', 't', 'p', '/', '1', '.', '1'})
	ext(0x0005, []byte{1, 0, 0, 0, 0})
	ext(0x000d, u16list(0x0403, 0x0804, 0x0401, 0x0503, 0x0805, 0x0501, 0x0806, 0x0601))
	ext(0x0012, nil)
	ext(0x0033, []byte{0, 0})
	ext(0x002d, []byte{1, 1})
	ext(0x002b, []byte{4, 0x0a, 0x0a, 0x03, 0x04})
	ext(0x001b, []byte{2, 0, 2})
	ext(0x4469, []byte{0, 3, 2, 'h', '2'})
	ext(0x0015, []byte{0, 0})

	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0)
	body = append(body, u16list(ciphers...)...)
	body = append(body, 1, 0)
	body = be.AppendUint16(body, uint16(len(exts)))
	body = append(body, exts...)
	hs := append([]byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
	return append([]byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}, hs...)
}

func TestTheFoxIOExample(t *testing.T) {
	got, ok := FromRecord(foxioHello())
	if !ok || got != foxioJA4 {
		t.Fatalf("JA4 = %q, %v; want %q", got, ok, foxioJA4)
	}
	if !Valid(got) {
		t.Errorf("Valid(%q) = false", got)
	}
	if _, ok := FromRecord([]byte{0x16, 0x03}); ok {
		t.Error("a truncated record parsed")
	}
}

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{
		foxioJA4:                               true,
		"q13d0310h3_55b375c5d22e_cd85d2d88918": true,
		"t13d1516h2_8daaf6152771_E5627EFA2AB1": false,
		"t13d1516h2_8daaf6152771":              false,
		"x13d1516h2_8daaf6152771_e5627efa2ab1": false,
		"t13d15 6h2_8daaf6152771_e5627efa2ab1": false,
		"t13d1516h2-8daaf6152771_e5627efa2ab1": false,
	} {
		if Valid(s) != want {
			t.Errorf("Valid(%q) = %v, want %v", s, !want, want)
		}
	}
	if !ValidJA3("e7d705a3286e19ea42f587b344ee6865") || ValidJA3("e7d705a3") || ValidJA3(strings.Repeat("g", 32)) {
		t.Error("ValidJA3 misjudges")
	}
}

// pipeConn serves the bytes of a test to a Conn.
func pipeConn(t *testing.T, chunks ...[]byte) *Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	go func() {
		for _, b := range chunks {
			_, _ = client.Write(b)
		}
		_ = client.Close()
	}()
	return &Conn{Conn: server}
}

func readAll(t *testing.T, c *Conn) []byte {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, 7) // smaller than a record, so the replay takes several reads
	for {
		n, err := c.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			return out.Bytes()
		}
	}
}

func TestConnCapturesAndReplaysTheHello(t *testing.T) {
	hello := foxioHello()
	tail := []byte("rest of the stream")
	c := pipeConn(t, hello[:9], hello[9:], tail)
	if got := readAll(t, c); !bytes.Equal(got, append(append([]byte{}, hello...), tail...)) {
		t.Fatalf("the stream changed: %d bytes, want %d", len(got), len(hello)+len(tail))
	}
	if c.JA4() != foxioJA4 {
		t.Errorf("JA4 = %q, want %q", c.JA4(), foxioJA4)
	}
}

func TestConnLeavesAnythingElseAlone(t *testing.T) {
	for name, wire := range map[string][]byte{
		"plain HTTP":       []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"),
		"a short header":   {0x16, 0x03},
		"an oversized one": {0x16, 0x03, 0x01, 0xea, 0x60, 1, 2, 3, 4},
		"a cut record":     foxioHello()[:40],
	} {
		t.Run(name, func(t *testing.T) {
			c := pipeConn(t, wire)
			if got := readAll(t, c); !bytes.Equal(got, wire) {
				t.Fatalf("the stream = %x, want %x", got, wire)
			}
			if c.JA4() != "" {
				t.Errorf("JA4 = %q, want none", c.JA4())
			}
		})
	}
}

func TestContext(t *testing.T) {
	ctx := context.Background()
	if JA4(ctx) != "" || JA3(ctx) != "" {
		t.Fatal("a bare context carries a fingerprint")
	}
	c := pipeConn(t, foxioHello())
	readAll(t, c)
	server, client := net.Pipe()
	t.Cleanup(func() { _, _ = server.Close(), client.Close() })
	if WithConn(ctx, server) != ctx {
		t.Error("WithConn changed the context for a connection it did not wrap")
	}
	ctx = WithConn(ctx, c)
	if JA4(ctx) != foxioJA4 {
		t.Errorf("JA4 = %q, want the captured one", JA4(ctx))
	}
	cdn := "t13d1517h2_8daaf6152771_b0da82dd1658"
	vctx := WithVouched(ctx, cdn, "e7d705a3286e19ea42f587b344ee6865")
	if JA4(vctx) != cdn || JA3(vctx) != "e7d705a3286e19ea42f587b344ee6865" {
		t.Errorf("JA4, JA3 = %q, %q; want the vouched ones", JA4(vctx), JA3(vctx))
	}
	if JA4(WithVouched(ctx, "", "e7d705a3286e19ea42f587b344ee6865")) != foxioJA4 {
		t.Error("an empty vouched JA4 hid the captured one")
	}
}
