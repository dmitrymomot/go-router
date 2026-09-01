package static

import (
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"syscall"

	"github.com/dmitrymomot/go-router/internal/nonseek"
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

// classifyFileError decides whether an open failure means "no such asset" or
// something the operator needs to hear about. A path that walks through a
// regular file, or past any limit the filesystem has, is a request for a file
// that cannot exist -- not a fault. The Dir backend reports those as ENOTDIR
// and ENAMETOOLONG, where the embedded one just says ErrNotExist; without this
// the two backends answered the same URL 500 and 404.
func classifyFileError(name string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist),
		errors.Is(err, syscall.ENOTDIR),
		errors.Is(err, syscall.ENAMETOOLONG),
		!fs.ValidPath(name) && errors.Is(err, fs.ErrInvalid):
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
		req = nonseek.Request(req, info.Size())
		var err error
		rs, err = nonseek.ReadSeeker("static: ", w.Header(), req, name, f, info.Size())
		if err != nil {
			return err
		}
	}

	http.ServeContent(w, req, path.Base(name), info.ModTime(), rs)
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
