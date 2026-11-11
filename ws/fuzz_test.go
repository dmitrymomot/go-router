package ws

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
	"unicode/utf8"
)

// FuzzFrameReader feeds arbitrary bytes to the frame reader. It must not
// panic, must not hand out a message over the limit or text that is not
// UTF-8, and must end every read with a message or an error it knows.
func FuzzFrameReader(f *testing.F) {
	euro := []byte("€")
	for _, seed := range [][]byte{
		frame(true, opText, []byte("hello")),
		frame(true, opBinary, make([]byte, 200)),
		append(frame(false, opText, euro[:2]), frame(true, opContinuation, euro[2:])...),
		append(frame(false, opBinary, []byte("a")), append(frame(true, opPing, []byte("p")), frame(true, opContinuation, []byte("b"))...)...),
		frame(true, opClose, closePayload(StatusNormalClosure, "bye")),
		frame(true, opPong, nil),
		rawFrame(true, 0, opText, false, []byte("x")),
		{finBit | opBinary, maskBit | 127, 0, 0, 0, 0, 0, 0, 0x10, 0},
	} {
		f.Add(seed)
	}
	const limit = 1024
	f.Fuzz(func(t *testing.T, data []byte) {
		r := &frameReader{
			br:     bufio.NewReader(bytes.NewReader(data)),
			limit:  limit,
			onPing: func(p []byte) error { return nil },
			onPong: func([]byte) {},
		}
		for {
			typ, msg, err := r.next()
			if err != nil {
				_, pe := errors.AsType[*protocolError](err)
				_, ce := errors.AsType[*CloseError](err)
				if !pe && !ce && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("next = %v, want a known error", err)
				}
				return
			}
			if len(msg) > limit {
				t.Fatalf("a message of %d bytes passed the limit of %d", len(msg), limit)
			}
			if typ == Text && !utf8.Valid(msg) {
				t.Fatalf("text %q is not UTF-8", msg)
			}
			if typ != Text && typ != Binary {
				t.Fatalf("message type %v", typ)
			}
			r.release()
		}
	})
}

func TestCheckUTF8(t *testing.T) {
	euro := "€"
	for _, tc := range []struct {
		in      string
		checked int
		done    bool
		want    int
		ok      bool
	}{
		{"abc", 0, true, 3, true},
		{"a" + euro[:2], 0, false, 1, true},
		{"a" + euro[:2], 0, true, 1, false},
		{"a" + euro, 1, true, 4, true},
		{"\xED\xA0", 0, false, 0, false}, // a surrogate can never complete
		{"\xF4\x90", 0, false, 0, false}, // over U+10FFFF
		{"\xC0", 0, false, 0, false},
		{"\xF0\x9F\x98", 0, false, 0, true},
	} {
		got, err := checkUTF8([]byte(tc.in), tc.checked, tc.done)
		if got != tc.want || (err == nil) != tc.ok {
			t.Errorf("checkUTF8(%q, %d, %v) = %d, %v; want %d, ok %v", tc.in, tc.checked, tc.done, got, err, tc.want, tc.ok)
		}
	}
}

func TestGrowAndRelease(t *testing.T) {
	b := grow(make([]byte, 2, 10), 3)
	if len(b) != 5 || cap(b) != 10 {
		t.Errorf("grow in place: len %d cap %d", len(b), cap(b))
	}
	r := &frameReader{buf: make([]byte, 0, maxKeptBuf+1)}
	r.release()
	if r.buf != nil {
		t.Error("release kept a large buffer")
	}
}
