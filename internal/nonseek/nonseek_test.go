package nonseek

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// forwardOnly hides the Seek method of the underlying reader, which is the
// whole point: an fs.File that cannot seek looks like this.
type forwardOnly struct{ r io.Reader }

func (f forwardOnly) Read(p []byte) (int, error) { return f.r.Read(p) }

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func serve(t *testing.T, method, name, rangeHeader string, body []byte, src io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/"+name, nil)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	req = Request(req, int64(len(body)))
	rec := httptest.NewRecorder()
	if src == nil {
		src = forwardOnly{bytes.NewReader(body)}
	}
	rs, err := ReadSeeker("test: ", rec.Header(), req, name, src, int64(len(body)))
	if err != nil {
		t.Fatalf("ReadSeeker: %v", err)
	}
	http.ServeContent(rec, req, name, time.Time{}, rs)
	return rec
}

func TestServeContentReadsAForwardOnlyFileWhole(t *testing.T) {
	body := []byte(strings.Repeat("go-router ", 200))
	rec := serve(t, http.MethodGet, "f.txt", "", body, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, body) {
		t.Errorf("body is %d bytes, want %d", len(got), len(body))
	}
}

// ServeContent sniffs the type from the first 512 bytes and then rewinds. The
// kept head is what answers that rewind.
func TestServeContentSniffsTheTypeAndStillSendsTheHead(t *testing.T) {
	body := append([]byte("<!DOCTYPE html>\n"), bytes.Repeat([]byte("x"), 1000)...)
	rec := serve(t, http.MethodGet, "page.unknownext", "", body, nil)

	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want a sniffed text/html", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Error("the sniffed head did not come back in the body")
	}
}

func TestServeContentAnswersHEADWithNoBody(t *testing.T) {
	body := []byte(strings.Repeat("a", 4096))
	rec := serve(t, http.MethodHead, "f.txt", "", body, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body is %d bytes, want none", rec.Body.Len())
	}
}

// A HEAD cannot sniff, and the extension says nothing, so ServeContent is told
// to leave the header out rather than guess.
func TestReadSeekerLeavesOutAContentTypeItCannotKnow(t *testing.T) {
	req := httptest.NewRequest(http.MethodHead, "/f.unknownext", nil)
	h := make(http.Header)
	if _, err := ReadSeeker("test: ", h, req, "f.unknownext", forwardOnly{strings.NewReader("body")}, 4); err != nil {
		t.Fatalf("ReadSeeker: %v", err)
	}
	value, ok := h["Content-Type"]
	if !ok || value != nil {
		t.Errorf("Content-Type entry = %v (present %v), want a present nil entry", value, ok)
	}
}

func TestServeContentAnswersARangeInsideTheSkipBudget(t *testing.T) {
	body := []byte(strings.Repeat("0123456789", 400))
	rec := serve(t, http.MethodGet, "f.txt", "bytes=2000-2009", body, nil)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPartialContent)
	}
	if got, want := rec.Body.String(), string(body[2000:2010]); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestRequestDropsARangeThisReaderCannotHonour(t *testing.T) {
	const size = 8 << 20
	cases := []struct {
		name, header string
		keep         bool
	}{
		{"several ranges", "bytes=0-1, 4-5", false},
		{"starts past the skip budget", "bytes=4000000-", false},
		{"starts inside the skip budget", "bytes=1024-2048", true},
		{"a suffix reaching back past the budget", "bytes=-100", false},
		{"a suffix inside the budget", "bytes=-8000000", true},
		{"a suffix longer than the file", "bytes=-99999999", true},
		{"not a byte range", "items=0-1", true},
		{"no dash", "bytes=12", true},
		{"an unreadable start", "bytes=abc-", true},
		{"a start past the end", "bytes=99999999-", true},
		{"an end before the start", "bytes=4000000-3", true},
		{"an unreadable end", "bytes=4000000-zz", true},
		{"an unreadable suffix", "bytes=-zz", true},
		{"a negative suffix", "bytes=--5", true},
		{"an empty suffix", "bytes=-", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/f.bin", nil)
			req.Header.Set("Range", tc.header)
			got := Request(req, size)
			if kept := got.Header.Get("Range") != ""; kept != tc.keep {
				t.Errorf("Range kept = %v, want %v", kept, tc.keep)
			}
			if req.Header.Get("Range") != tc.header {
				t.Error("Request rewrote the caller's own request")
			}
		})
	}
}

// A HEAD never reads a body, so no Range on it can cost a skip.
func TestRequestKeepsAFarRangeOnHEAD(t *testing.T) {
	req := httptest.NewRequest(http.MethodHead, "/f.bin", nil)
	req.Header.Set("Range", "bytes=4000000-")
	if got := Request(req, 8<<20); got.Header.Get("Range") == "" {
		t.Error("Request dropped the Range of a HEAD")
	}
}

func TestReadSeekerRejectsANegativeSize(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/f.bin", nil)
	_, err := ReadSeeker("test: ", make(http.Header), req, "f.bin", strings.NewReader(""), -1)
	if err == nil || !strings.HasPrefix(err.Error(), "test: ") {
		t.Errorf("err = %v, want one carrying the caller's prefix", err)
	}
}

func newReader(t *testing.T, method string, body []byte) io.ReadSeeker {
	t.Helper()
	req := httptest.NewRequest(method, "/f.bin", nil)
	rs, err := ReadSeeker("test: ", make(http.Header), req, "f.bin", forwardOnly{bytes.NewReader(body)}, int64(len(body)))
	if err != nil {
		t.Fatalf("ReadSeeker: %v", err)
	}
	return rs
}

func TestSeekRefusesWhatAForwardOnlyFileCannotDo(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 4096)

	cases := []struct {
		name   string
		seek   func(rs io.ReadSeeker) error
		errHas string
	}{
		{
			name:   "an origin that is not one of the three",
			seek:   func(rs io.ReadSeeker) error { _, err := rs.Seek(0, 42); return err },
			errHas: "invalid seek origin",
		},
		{
			name:   "past the end",
			seek:   func(rs io.ReadSeeker) error { _, err := rs.Seek(9000, io.SeekStart); return err },
			errHas: "seek outside the file",
		},
		{
			name:   "before the start",
			seek:   func(rs io.ReadSeeker) error { _, err := rs.Seek(-1, io.SeekStart); return err },
			errHas: "seek outside the file",
		},
		{
			name: "back past the head that was kept",
			seek: func(rs io.ReadSeeker) error {
				if _, err := rs.Seek(2000, io.SeekStart); err != nil {
					return err
				}
				if _, err := rs.Read(make([]byte, 16)); err != nil {
					return err
				}
				_, err := rs.Seek(1000, io.SeekStart)
				return err
			},
			errHas: "cannot rewind a non-seekable file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.seek(newReader(t, http.MethodGet, body))
			if err == nil || !strings.Contains(err.Error(), tc.errHas) {
				t.Errorf("err = %v, want one naming %q", err, tc.errHas)
			}
		})
	}
}

func TestSeekFromTheCurrentPositionAndTheEnd(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 100)
	rs := newReader(t, http.MethodGet, body)

	if got, err := rs.Seek(-10, io.SeekEnd); err != nil || got != 90 {
		t.Fatalf("Seek(-10, SeekEnd) = %d, %v; want 90, nil", got, err)
	}
	if got, err := rs.Seek(5, io.SeekCurrent); err != nil || got != 95 {
		t.Fatalf("Seek(5, SeekCurrent) = %d, %v; want 95, nil", got, err)
	}
	rest, err := io.ReadAll(rs)
	if err != nil || len(rest) != 5 {
		t.Fatalf("read %d bytes, %v; want 5, nil", len(rest), err)
	}
}

// The one-byte probe on the first SeekEnd is there so a source that cannot be
// read at all says so while ServeContent can still turn it into a 500.
func TestSeekEndProbesTheSourceOnce(t *testing.T) {
	want := errors.New("no")
	req := httptest.NewRequest(http.MethodGet, "/f.bin", nil)
	rs, err := ReadSeeker("test: ", make(http.Header), req, "f.bin", failingReader{err: want}, 100)
	if err != nil {
		t.Fatalf("ReadSeeker: %v", err)
	}
	if _, err := rs.Seek(0, io.SeekEnd); !errors.Is(err, want) {
		t.Errorf("Seek(0, SeekEnd) err = %v, want %v", err, want)
	}
	if _, err := rs.Seek(0, io.SeekStart); !errors.Is(err, want) {
		t.Errorf("a later Seek err = %v, want the read error again", err)
	}
}

func TestReadStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	req := httptest.NewRequest(http.MethodGet, "/f.bin", nil).WithContext(ctx)
	rs, err := ReadSeeker("test: ", make(http.Header), req, "f.bin", forwardOnly{bytes.NewReader([]byte("body"))}, 4)
	if err != nil {
		t.Fatalf("ReadSeeker: %v", err)
	}
	if _, err := rs.Read(make([]byte, 4)); !errors.Is(err, context.Canceled) {
		t.Errorf("Read err = %v, want context.Canceled", err)
	}
}

// A source that ends early cannot reach the offset the Range asked for.
func TestReadReportsASourceThatStopsShort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/f.bin", nil)
	rs, err := ReadSeeker("test: ", make(http.Header), req, "f.bin", forwardOnly{bytes.NewReader(nil)}, 4096)
	if err != nil {
		t.Fatalf("ReadSeeker: %v", err)
	}
	if _, err := rs.Seek(2000, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if _, err := rs.Read(make([]byte, 16)); err == nil {
		t.Error("Read past the end of a short source reported no error")
	}
}
