package static

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
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
	if !errors.Is(err, errNoFile) || !a.fallback(r, name) {
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

func (a *Assets) fallback(r *http.Request, name string) bool {
	if !a.spa || name == a.index {
		return false
	}
	if a.isNavigation != nil {
		return a.isNavigation(r)
	}
	if path.Ext(name) == "" {
		return true
	}
	for v := range strings.SplitSeq(r.Header.Get("Accept"), ",") {
		media, _, _ := strings.Cut(v, ";")
		if strings.EqualFold(strings.TrimSpace(media), "text/html") {
			return true
		}
	}
	return false
}

func (a *Assets) write(w http.ResponseWriter, r *http.Request, name string, versioned bool) error {
	f, info, err := a.openFile(name)
	if err != nil {
		return errNoFile
	}
	//nolint:errcheck // The file is read only.
	defer f.Close()
	if info.IsDir() {
		idx := path.Join(name, a.index)
		if a.redirectDir && !strings.HasSuffix(r.URL.Path, "/") && a.Has(idx) {
			redirectDir(w, r)
			return nil
		}
		return a.write(w, r, idx, versioned)
	}
	return a.send(w, r, name, f, info, versioned)
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
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		rs = bytes.NewReader(data)
	}

	h := w.Header()
	if tag := a.etag(name, info); tag != "" {
		h.Set("Etag", tag)
	}
	h.Set("Cache-Control", a.cacheControl(name, versioned))

	http.ServeContent(w, r, path.Base(name), info.ModTime(), rs)
	return nil
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
