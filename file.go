package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	dispositionAttachment = "attachment"
	dispositionInline     = "inline"
)

func (b *Base) File(name string) error {
	return b.serveFile(name, nil, "", "")
}

func (b *Base) FileFS(name string, fsys fs.FS) error {
	if fsys == nil {
		return ErrInternalServerError.WithError(errors.New("router: FileFS needs a file system"))
	}
	return b.serveFile(name, fsys, "", "")
}

func (b *Base) AttachmentFile(name, filename string) error {
	return b.serveFile(name, nil, dispositionAttachment, filename)
}

func (b *Base) InlineFile(name, filename string) error {
	return b.serveFile(name, nil, dispositionInline, filename)
}

func (b *Base) Inline(status int, contentType, filename string, data []byte) error {
	b.res.Header().Set(HeaderContentDisposition, contentDisposition(dispositionInline, filename))
	return b.Blob(status, contentType, data)
}

func (b *Base) serveFile(name string, fsys fs.FS, kind, filename string) error {
	if !safeFileName(name) {
		return ErrNotFound
	}
	clean := cleanFileName(name)
	if clean == "." || !fs.ValidPath(clean) {
		return ErrNotFound
	}

	f, err := openFileIn(clean, fsys)
	if err != nil {
		return fileNotFound(err)
	}
	//nolint:errcheck // The file is read only.
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fileNotFound(err)
	}
	if info.IsDir() {
		return ErrNotFound
	}

	if kind != "" {
		if filename == "" {
			filename = path.Base(clean)
		}
		b.res.Header().Set(HeaderContentDisposition, contentDisposition(kind, filename))
	}
	return b.sendFile(clean, f, info)
}

// os.Root refuses a path that leaves the directory, symbolic links included.
func openFileIn(name string, fsys fs.FS) (fs.File, error) {
	if fsys != nil {
		return fsys.Open(name)
	}
	return os.OpenInRoot(".", filepath.FromSlash(name))
}

func fileNotFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return ErrNotFound.WithError(fmt.Errorf("router: serve the file: %w", err))
}

func (b *Base) sendFile(name string, f fs.File, info fs.FileInfo) error {
	req := b.req
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		req = nonSeekableRequest(req, info.Size())
		var err error
		rs, err = forwardFile(b.res.Header(), req, name, f, info.Size())
		if err != nil {
			return ErrInternalServerError.WithError(fmt.Errorf("router: read the file: %w", err))
		}
	}

	http.ServeContent(b.res, req, path.Base(name), info.ModTime(), rs)
	return nil
}

const maxNonSeekableRangeSkip int64 = 1 << 20

func nonSeekableRequest(r *http.Request, size int64) *http.Request {
	rangeHeader := r.Header.Get("Range")
	ignore := strings.Contains(rangeHeader, ",")
	if !ignore && r.Method != http.MethodHead {
		if start, ok := singleRangeStart(rangeHeader, size); ok && start > maxNonSeekableRangeSkip {
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

func forwardFile(h http.Header, r *http.Request, name string, src io.Reader, size int64) (io.ReadSeeker, error) {
	if size < 0 {
		return nil, errors.New("negative file size")
	}
	if _, ok := h[HeaderContentType]; !ok && r.Method == http.MethodHead && mime.TypeByExtension(path.Ext(name)) == "" {
		h[HeaderContentType] = nil
	}
	return &forwardReadSeeker{r: src, ctx: r.Context(), size: size, probe: r.Method != http.MethodHead}, nil
}

type forwardReadSeeker struct {
	r         io.Reader
	ctx       context.Context
	size      int64
	pos       int64
	sourcePos int64
	target    int64
	prefix    []byte
	readErr   error
	probe     bool
}

func (s *forwardReadSeeker) Read(p []byte) (int, error) {
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
	if s.pos < int64(len(s.prefix)) {
		n := copy(p, s.prefix[s.pos:])
		s.pos += int64(n)
		s.target = s.pos
		if s.pos == s.size {
			return n, io.EOF
		}
		return n, nil
	}
	if s.pos != s.sourcePos {
		return 0, errors.New("cannot rewind a non-seekable file")
	}
	n, err := s.readSource(p)
	s.pos = s.sourcePos
	s.target = s.pos
	if err == nil && s.pos == s.size {
		err = io.EOF
	}
	return n, err
}

func (s *forwardReadSeeker) move() error {
	if s.target == s.pos {
		return nil
	}
	if s.target <= int64(len(s.prefix)) {
		s.pos = s.target
		return nil
	}
	if s.target < s.sourcePos {
		return errors.New("cannot rewind a non-seekable file")
	}
	skip := s.target - s.sourcePos
	if skip > maxNonSeekableRangeSkip {
		return errors.New("range starts too late for a non-seekable file")
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

func (s *forwardReadSeeker) readSource(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		s.readErr = err
	}
	if keep := min(n, 512-len(s.prefix)); keep > 0 {
		s.prefix = append(s.prefix, p[:keep]...)
	}
	s.sourcePos += int64(n)
	return n, err
}

func (s *forwardReadSeeker) Seek(offset int64, whence int) (int64, error) {
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
		return 0, errors.New("invalid seek origin")
	}
	target := base + offset
	if offset > 0 && target < base || offset < 0 && target > base || target < 0 || target > s.size {
		return 0, errors.New("seek outside the file")
	}
	if target < s.sourcePos && target > int64(len(s.prefix)) {
		return 0, errors.New("cannot rewind a non-seekable file")
	}
	s.target = target
	return target, nil
}

func safeFileName(name string) bool {
	if name == "" || name == "." || path.IsAbs(name) || filepath.IsAbs(name) ||
		filepath.VolumeName(name) != "" || strings.ContainsRune(name, '\\') {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func cleanFileName(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

func contentDisposition(kind, filename string) string {
	name := lastSegment(filename)
	if name == "" {
		return kind
	}
	ascii := asciiFileName(name)
	if ascii == name {
		return kind + `; filename="` + ascii + `"`
	}
	return kind + `; filename="` + ascii + `"; filename*=UTF-8''` + encodeExtValue(name)
}

func lastSegment(filename string) string {
	if i := strings.LastIndexAny(filename, `/\`); i >= 0 {
		filename = filename[i+1:]
	}
	if filename == "." || filename == ".." {
		return ""
	}
	return filename
}

func asciiFileName(name string) string {
	var sb strings.Builder
	sb.Grow(len(name))
	for i := range len(name) {
		switch c := name[i]; {
		case c < 0x20, c >= 0x7f, c == '"', c == '\\':
			sb.WriteByte('_')
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

const hexDigits = "0123456789ABCDEF"

func encodeExtValue(name string) string {
	var sb strings.Builder
	sb.Grow(len(name))
	for i := range len(name) {
		c := name[i]
		if isAttrChar(c) {
			sb.WriteByte(c)
			continue
		}
		sb.WriteByte('%')
		sb.WriteByte(hexDigits[c>>4])
		sb.WriteByte(hexDigits[c&0x0f])
	}
	return sb.String()
}

func isAttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	default:
		return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
	}
}
