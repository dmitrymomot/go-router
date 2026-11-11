package ws

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"unicode/utf8"
)

// The opcodes of RFC 6455, section 5.2.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

const (
	finBit  = 0x80
	rsvBits = 0x70
	maskBit = 0x80

	// maxControlPayload is the largest payload of a control frame.
	maxControlPayload = 125
	// maxCloseReason is the longest reason a close frame holds, after the two
	// bytes of its code.
	maxCloseReason = maxControlPayload - 2
)

// protocolError is a frame the peer sent against the rules. The connection
// answers it with a close frame that carries code.
type protocolError struct {
	msg  string
	code StatusCode
}

func (e *protocolError) Error() string { return "ws: " + e.msg }

func violation(code StatusCode, format string, args ...any) *protocolError {
	return &protocolError{code: code, msg: fmt.Sprintf(format, args...)}
}

// frameReader reads the frames of a client and joins them into messages. It
// answers no frame itself: ping, pong and close reach the callbacks.
type frameReader struct {
	br *bufio.Reader

	// beforeFrame runs before the header of each frame is read. The
	// connection sets its read deadline there.
	beforeFrame func()
	// onPing and onPong get the payload of a control frame, which is valid
	// only during the call.
	onPing func(payload []byte)
	onPong func(payload []byte)

	buf     []byte
	limit   int64
	control [maxControlPayload]byte
}

// header is the part of a frame before its payload.
type header struct {
	length int64
	mask   [4]byte
	fin    bool
	opcode byte
}

func (r *frameReader) readHeader() (header, error) {
	var h header
	var b [8]byte
	if _, err := io.ReadFull(r.br, b[:2]); err != nil {
		return h, err
	}
	h.fin = b[0]&finBit != 0
	h.opcode = b[0] & 0x0F
	if b[0]&rsvBits != 0 {
		// No extension is negotiated, so no reserved bit may be set.
		return h, violation(StatusProtocolError, "a frame sets a reserved bit")
	}
	switch h.opcode {
	case opContinuation, opText, opBinary, opClose, opPing, opPong:
	default:
		return h, violation(StatusProtocolError, "a frame uses the reserved opcode %#x", h.opcode)
	}
	if b[1]&maskBit == 0 {
		return h, violation(StatusProtocolError, "a client frame is not masked")
	}
	switch n := b[1] &^ maskBit; n {
	case 126:
		if _, err := io.ReadFull(r.br, b[:2]); err != nil {
			return h, err
		}
		h.length = int64(binary.BigEndian.Uint16(b[:2]))
	case 127:
		if _, err := io.ReadFull(r.br, b[:8]); err != nil {
			return h, err
		}
		v := binary.BigEndian.Uint64(b[:8])
		if v>>63 != 0 {
			return h, violation(StatusProtocolError, "a frame length sets its most significant bit")
		}
		h.length = int64(v)
	default:
		h.length = int64(n)
	}
	if h.opcode >= opClose {
		if !h.fin {
			return h, violation(StatusProtocolError, "a control frame is fragmented")
		}
		if h.length > maxControlPayload {
			return h, violation(StatusProtocolError, "a control frame carries %d bytes, over %d", h.length, maxControlPayload)
		}
	}
	if _, err := io.ReadFull(r.br, h.mask[:]); err != nil {
		return h, err
	}
	return h, nil
}

// unmask applies mask to p, whose first byte sits at offset pos of the
// payload. It works eight bytes at a time.
func unmask(p []byte, mask [4]byte, pos int) {
	if k := pos & 3; k != 0 {
		mask = [4]byte{mask[k], mask[(k+1)&3], mask[(k+2)&3], mask[(k+3)&3]}
	}
	m32 := uint64(binary.LittleEndian.Uint32(mask[:]))
	m := m32 | m32<<32
	for len(p) >= 8 {
		binary.LittleEndian.PutUint64(p, binary.LittleEndian.Uint64(p)^m)
		p = p[8:]
	}
	for i := range p {
		p[i] ^= mask[i&3]
	}
}

// next reads frames until a whole data message arrives, and reports its type
// and payload. The payload belongs to the caller. A close frame ends
// the read with a *CloseError; a frame against the rules, with a
// *protocolError.
func (r *frameReader) next() (MessageType, []byte, error) {
	var typ MessageType
	started := false
	checked := 0 // the prefix of buf, in bytes, that is valid UTF-8
	r.buf = r.buf[:0]
	for {
		if r.beforeFrame != nil {
			r.beforeFrame()
		}
		h, err := r.readHeader()
		if err != nil {
			return 0, nil, err
		}
		switch h.opcode {
		case opClose, opPing, opPong:
			if err := r.readControl(h); err != nil {
				return 0, nil, err
			}
			continue
		case opContinuation:
			if !started {
				return 0, nil, violation(StatusProtocolError, "a continuation frame starts no message")
			}
		default:
			if started {
				return 0, nil, violation(StatusProtocolError, "a data frame starts a message inside another")
			}
			started = true
			typ = MessageType(h.opcode)
		}

		if h.length > r.limit-int64(len(r.buf)) {
			return 0, nil, violation(StatusMessageTooBig, "a message exceeds the read limit of %d bytes", r.limit)
		}
		if r.buf, err = r.appendPayload(r.buf, h.length, h.mask); err != nil {
			return 0, nil, err
		}
		if typ == Text {
			if checked, err = checkUTF8(r.buf, checked, h.fin); err != nil {
				return 0, nil, err
			}
		}
		if h.fin {
			// The payload belongs to the caller, and the next message gets a
			// new buffer.
			data := r.buf
			r.buf = nil
			return typ, data, nil
		}
	}
}

// payloadChunk is the most that appendPayload grows its buffer by before the
// bytes arrive, so a header that claims a large frame costs memory only as
// fast as the client sends it.
const payloadChunk = 64 << 10

// appendPayload reads a data payload of n bytes onto b and unmasks it.
func (r *frameReader) appendPayload(b []byte, n int64, mask [4]byte) ([]byte, error) {
	for pos := 0; n > 0; {
		k := int(min(n, payloadChunk))
		start := len(b)
		b = slices.Grow(b, k)[:start+k]
		p := b[start:]
		if _, err := io.ReadFull(r.br, p); err != nil {
			return b, err
		}
		unmask(p, mask, pos)
		pos += k
		n -= int64(k)
	}
	return b, nil
}

func (r *frameReader) readControl(h header) error {
	// control holds the largest control payload, so this does not allocate.
	p, err := r.appendPayload(r.control[:0], h.length, h.mask)
	if err != nil {
		return err
	}
	switch h.opcode {
	case opPing:
		r.onPing(p)
	case opPong:
		r.onPong(p)
	default:
		return parseClose(p)
	}
	return nil
}

// parseClose reads the payload of a close frame into a *CloseError, or reports
// the rule it breaks.
func parseClose(p []byte) error {
	switch len(p) {
	case 0:
		return &CloseError{Code: StatusNoStatusReceived}
	case 1:
		return violation(StatusProtocolError, "a close frame carries one byte")
	}
	code := StatusCode(binary.BigEndian.Uint16(p))
	if !code.valid() {
		return violation(StatusProtocolError, "a close frame carries the code %d", code)
	}
	reason := p[2:]
	if !utf8.Valid(reason) {
		return violation(StatusInvalidPayload, "the reason of a close frame is not valid UTF-8")
	}
	return &CloseError{Code: code, Reason: string(reason)}
}

// checkUTF8 checks b from checked on, and reports the length of the prefix
// that holds whole valid characters. A message that is not done may end inside
// a character, which the next frame completes; one that is done may not.
//
// utf8.FullRune counts an invalid sequence as full, so a sequence it reports
// as not full is the valid start of a character.
func checkUTF8(b []byte, checked int, done bool) (int, error) {
	if utf8.Valid(b[checked:]) {
		// The fast path: whole valid characters to the end.
		return len(b), nil
	}
	i := checked
	for i < len(b) {
		if b[i] < utf8.RuneSelf {
			i++
			continue
		}
		if !utf8.FullRune(b[i:]) {
			if done {
				break
			}
			return i, nil
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		i += size
	}
	if i == len(b) {
		return i, nil
	}
	return i, violation(StatusInvalidPayload, "a text message is not valid UTF-8")
}

// appendHeader appends the header of an unmasked, final server frame.
func appendHeader(b []byte, opcode byte, length int) []byte {
	b0 := opcode | finBit
	switch {
	case length <= 125:
		return append(b, b0, byte(length)) //nolint:gosec // length is at most 125.
	case length <= 0xFFFF:
		return binary.BigEndian.AppendUint16(append(b, b0, 126), uint16(length))
	default:
		return binary.BigEndian.AppendUint64(append(b, b0, 127), uint64(length))
	}
}
