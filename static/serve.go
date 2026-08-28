package static

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

var errNoFile = errors.New("static: the asset set holds no such file")

var errMethod = errors.New("static: the method is neither GET nor HEAD")

const allowedMethods = "GET, HEAD"

func (a *Assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := a.serve(w, r, r.URL.Path)
	switch {
	case err == nil:
	case errors.Is(err, errMethod):
		w.Header().Set("Allow", allowedMethods)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	case errors.Is(err, errNoFile):
		a.notFound.ServeHTTP(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func (a *Assets) serve(w http.ResponseWriter, r *http.Request, upath string) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return errMethod
	}

	name, versioned := a.resolve(upath)
	err := a.write(w, r, name, versioned)
	if !errors.Is(err, errNoFile) || !a.fallback(w, r, name) {
		return err
	}
	return a.write(w, r, a.index, false)
}

func (a *Assets) resolve(upath string) (name string, versioned bool) {
	name = cleanName(upath)
	if a.build != "" {
		if rest, ok := cutSegment(name, a.build); ok {
			name, versioned = rest, true
		}
	}
	if name == "" {
		name = a.index
	}
	return name, versioned
}

func cutSegment(name, seg string) (string, bool) {
	if name == seg {
		return "", true
	}
	return strings.CutPrefix(name, seg+"/")
}

func (a *Assets) fallback(w http.ResponseWriter, r *http.Request, name string) bool {
	if !a.spa || name == a.index {
		return false
	}
	if a.isNavigation != nil {
		w.Header().Set("Vary", "*")
		return a.isNavigation(r)
	}
	addVary(w.Header(), "Accept")
	accept := strings.Join(r.Header.Values("Accept"), ",")
	if strings.TrimSpace(accept) == "" {
		return path.Ext(name) == ""
	}
	specificity, quality := htmlPreference(accept)
	if path.Ext(name) == "" {
		return specificity >= 0 && quality > 0
	}
	return specificity == 2 && quality > 0
}

func htmlPreference(accept string) (int, float64) {
	bestSpecificity, bestQuality := -1, 0.0
	for v := range strings.SplitSeq(accept, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(v))
		if err != nil {
			continue
		}
		var specificity int
		switch strings.ToLower(media) {
		case "text/html":
			specificity = 2
		case "text/*":
			specificity = 1
		case "*/*":
			specificity = 0
		default:
			continue
		}
		quality := 1.0
		if raw, ok := params["q"]; ok {
			quality, err = strconv.ParseFloat(raw, 64)
			if err != nil || quality < 0 || quality > 1 {
				continue
			}
		}
		if specificity > bestSpecificity || specificity == bestSpecificity && quality > bestQuality {
			bestSpecificity, bestQuality = specificity, quality
		}
	}
	return bestSpecificity, bestQuality
}

func addVary(h http.Header, name string) {
	if h.Get("Vary") == "*" {
		return
	}
	for value := range strings.SplitSeq(h.Get("Vary"), ",") {
		if strings.EqualFold(strings.TrimSpace(value), name) {
			return
		}
	}
	h.Add("Vary", name)
}

func (a *Assets) write(w http.ResponseWriter, r *http.Request, name string, versioned bool) error {
	return a.writePath(w, r, name, versioned, true)
}

func (a *Assets) writePath(
	w http.ResponseWriter,
	r *http.Request,
	name string,
	versioned bool,
	allowDirectoryIndex bool,
) error {
	f, info, err := a.openFile(name)
	if err != nil {
		return classifyFileError(name, err)
	}
	//nolint:errcheck // The file is read only.
	defer f.Close()
	if info.IsDir() {
		if !allowDirectoryIndex {
			return errNoFile
		}
		idx := path.Join(name, a.index)
		if idx == name {
			return errNoFile
		}
		if a.redirectDir && !strings.HasSuffix(r.URL.Path, "/") && a.Has(idx) {
			redirectDir(w, r)
			return nil
		}
		return a.writePath(w, r, idx, versioned, false)
	}
	return a.send(w, r, name, f, info, versioned)
}

func classifyFileError(name string, err error) error {
	if errors.Is(err, fs.ErrNotExist) || !fs.ValidPath(name) && errors.Is(err, fs.ErrInvalid) {
		return errNoFile
	}
	return err
}

func redirectDir(w http.ResponseWriter, r *http.Request) {
	dest := "./" + path.Base(r.URL.EscapedPath()) + "/"
	if q := r.URL.RawQuery; q != "" {
		dest += "?" + q
	}
	w.Header().Set("Location", dest)
	w.WriteHeader(http.StatusMovedPermanently)
}

func (a *Assets) send(w http.ResponseWriter, r *http.Request, name string, f fs.File, info fs.FileInfo, versioned bool) error {
	h := w.Header()
	if tag := a.etag(name, info); tag != "" {
		h.Set("Etag", tag)
	}
	h.Set("Cache-Control", a.cacheControl(name, versioned))

	req := r
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		req = nonSeekableRequest(req, info.Size())
		var err error
		rs, err = forwardFile(w.Header(), req, name, f, info.Size())
		if err != nil {
			return err
		}
	}

	http.ServeContent(w, req, path.Base(name), info.ModTime(), rs)
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
		return nil, errors.New("static: negative file size")
	}
	if _, ok := h["Content-Type"]; !ok && r.Method == http.MethodHead && mime.TypeByExtension(path.Ext(name)) == "" {
		h["Content-Type"] = nil
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
		return 0, errors.New("static: cannot rewind a non-seekable file")
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
		return errors.New("static: cannot rewind a non-seekable file")
	}
	skip := s.target - s.sourcePos
	if skip > maxNonSeekableRangeSkip {
		return errors.New("static: range starts too late for a non-seekable file")
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
		return 0, errors.New("static: invalid seek origin")
	}
	target := base + offset
	if offset > 0 && target < base || offset < 0 && target > base || target < 0 || target > s.size {
		return 0, errors.New("static: seek outside the file")
	}
	if target < s.sourcePos && target > int64(len(s.prefix)) {
		return 0, errors.New("static: cannot rewind a non-seekable file")
	}
	s.target = target
	return target, nil
}

func (a *Assets) openFile(name string) (fs.File, fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, nil, fs.ErrInvalid
	}
	f, err := a.fsys.Open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		//nolint:errcheck // The open already failed to be useful.
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

func (a *Assets) etag(name string, info fs.FileInfo) string {
	if a.etags != nil {
		return a.etags[name]
	}
	return `W/"` + strconv.FormatInt(info.Size(), 16) + "-" +
		strconv.FormatInt(info.ModTime().UnixNano(), 16) + `"`
}

func (a *Assets) cacheControl(name string, versioned bool) string {
	switch {
	case path.Base(name) == path.Base(a.index):
		return "no-cache"
	case versioned:
		return immutableCacheControl
	default:
		return a.cache
	}
}
