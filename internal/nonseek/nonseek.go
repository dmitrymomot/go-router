// Package nonseek serves a file whose reader cannot seek.
//
// http.ServeContent needs an io.ReadSeeker, and an fs.File is only an
// io.Reader. The type here fakes the seeking, and the shape of the fake is
// dictated by the exact order in which ServeContent seeks and reads:
//
//   - It seeks to the end to learn the size, then back to the start. Nothing is
//     read yet, so the rewind is free. The one-byte probe on that first
//     SeekEnd is there to surface a read error while ServeContent can still
//     turn it into a 500, rather than half-way through a body it has already
//     committed to. A HEAD request never reads a body, so it does not probe.
//   - It sniffs the content type from the first 512 bytes and rewinds. Those
//     bytes are kept, so that one rewind can be answered from memory. It is the
//     only rewind this reader can serve: anything further back fails.
//   - A Range that starts later than MaxRangeSkip is refused rather than paid
//     for by reading and discarding the gap. Request drops such a header before
//     ServeContent sees it, so the answer is the whole file instead of an
//     error.
//
// Both the router's Context.File and the static asset server go through here.
// Two copies of this choreography would drift, and the drift would be silent.
package nonseek

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// ReadSeeker trusts size. It caps every read at size - pos and reports size for
// a seek to the end, because a reader that cannot seek has no other way to know
// where the file ends. Content-Length is therefore whatever Stat said: a size
// too small truncates the body without an error, and one too large leaves the
// client waiting for bytes that never come. A regular file always reports its
// own length, but a synthetic fs.FS that computes one has to make it match the
// bytes it will hand over.

// MaxRangeSkip is the furthest into a non-seekable file a Range may start.
// Reaching a later offset means reading and throwing away everything before it.
const MaxRangeSkip int64 = 1 << 20

// Request drops a Range header that this reader cannot honour, so that
// ServeContent answers with the whole file rather than an error. The request is
// cloned: the caller's own request keeps its headers.
func Request(r *http.Request, size int64) *http.Request {
	rangeHeader := r.Header.Get("Range")
	ignore := strings.Contains(rangeHeader, ",")
	if !ignore && r.Method != http.MethodHead {
		if start, ok := singleRangeStart(rangeHeader, size); ok && start > MaxRangeSkip {
			ignore = true
		}
	}
	if !ignore {
		return r
	}
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	clone.Header.Del("Range")
	return clone
}

func singleRangeStart(value string, size int64) (int64, bool) {
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, false
	}
	start, end, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(value, "bytes=")), "-")
	if !ok {
		return 0, false
	}
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" {
		if end == "" || strings.HasPrefix(end, "-") {
			return 0, false
		}
		suffix, err := strconv.ParseInt(end, 10, 64)
		if err != nil || suffix < 0 {
			return 0, false
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, true
	}
	first, err := strconv.ParseInt(start, 10, 64)
	if err != nil || first < 0 || first >= size {
		return 0, false
	}
	if end != "" {
		last, err := strconv.ParseInt(end, 10, 64)
		if err != nil || first > last {
			return 0, false
		}
	}
	return first, true
}

// ReadSeeker wraps src so ServeContent can serve it. Every error it reports
// carries prefix, because ServeContent puts the text in the response.
// Reader is the fake seeker. Err reports a read failure that ServeContent
// turned into its own response, so the caller can put it back on the error path.
type Reader struct{ reader }

// Err is the read error the source returned, if any. ServeContent answers a
// failed Seek with its own 500 in text/plain and tells the caller nothing, so
// without this the router's error handler, logger and Observe never learned
// that the file could not be read.
func (r *Reader) Err() error { return r.readErr }

func ReadSeeker(prefix string, h http.Header, r *http.Request, name string, src io.Reader, size int64) (*Reader, error) {
	if size < 0 {
		return nil, errors.New(prefix + "negative file size")
	}
	// ServeContent sniffs a missing type from the body, and a HEAD has no body
	// to sniff. A nil entry tells it to leave the header out rather than guess.
	if _, ok := h["Content-Type"]; !ok && r.Method == http.MethodHead && mime.TypeByExtension(path.Ext(name)) == "" {
		h["Content-Type"] = nil
	}
	return &Reader{reader{r: src, ctx: r.Context(), size: size, probe: r.Method != http.MethodHead, errPrefix: prefix}}, nil
}

type reader struct {
	r         io.Reader
	ctx       context.Context
	size      int64
	pos       int64
	sourcePos int64
	target    int64
	head      []byte
	errPrefix string
	readErr   error
	probe     bool
}

func (s *reader) fail(msg string) error { return errors.New(s.errPrefix + msg) }

func (s *reader) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if err := s.move(); err != nil {
		return 0, err
	}
	remain := s.size - s.pos
	if remain <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > remain {
		p = p[:remain]
	}
	if s.pos < int64(len(s.head)) {
		n := copy(p, s.head[s.pos:])
		s.pos += int64(n)
		s.target = s.pos
		if s.pos == s.size {
			return n, io.EOF
		}
		return n, nil
	}
	if s.pos != s.sourcePos {
		return 0, s.fail("cannot rewind a non-seekable file")
	}
	n, err := s.readSource(p)
	s.pos = s.sourcePos
	s.target = s.pos
	if err == nil && s.pos == s.size {
		err = io.EOF
	}
	return n, err
}

func (s *reader) move() error {
	if s.target == s.pos {
		return nil
	}
	if s.target <= int64(len(s.head)) {
		s.pos = s.target
		return nil
	}
	if s.target < s.sourcePos {
		return s.fail("cannot rewind a non-seekable file")
	}
	skip := s.target - s.sourcePos
	if skip > MaxRangeSkip {
		return s.fail("range starts too late for a non-seekable file")
	}
	var buf [32 * 1024]byte
	for skip > 0 {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		chunk := min(int64(len(buf)), skip)
		n, err := s.readSource(buf[:chunk])
		skip -= int64(n)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	s.pos = s.sourcePos
	s.target = s.pos
	return nil
}

func (s *reader) readSource(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		s.readErr = err
	}
	if keep := min(n, 512-len(s.head)); keep > 0 {
		s.head = append(s.head, p[:keep]...)
	}
	s.sourcePos += int64(n)
	return n, err
}

func (s *reader) Seek(offset int64, whence int) (int64, error) {
	if s.readErr != nil {
		return 0, s.readErr
	}
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = s.target
	case io.SeekEnd:
		if s.probe && s.size > 0 && s.sourcePos == 0 && s.ctx.Err() == nil {
			var buf [1]byte
			if _, err := s.readSource(buf[:]); err != nil && !errors.Is(err, io.EOF) {
				return 0, err
			}
		}
		base = s.size
	default:
		return 0, s.fail("invalid seek origin")
	}
	target := base + offset
	if offset > 0 && target < base || offset < 0 && target > base || target < 0 || target > s.size {
		return 0, s.fail("seek outside the file")
	}
	if target < s.sourcePos && target > int64(len(s.head)) {
		return 0, s.fail("cannot rewind a non-seekable file")
	}
	s.target = target
	return target, nil
}
