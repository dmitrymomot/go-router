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

// errNoFile reports that the asset set holds no file for the request. The
// caller decides what a miss looks like: [Assets.ServeHTTP] runs Config.NotFound
// and [Handler] returns the not-found error of the router.
var errNoFile = errors.New("static: the asset set holds no such file")

// errMethod reports a method other than GET or HEAD.
var errMethod = errors.New("static: the method is neither GET nor HEAD")

// allowedMethods is the Allow header of an answer to any other method.
const allowedMethods = "GET, HEAD"

// ServeHTTP implements [http.Handler]. The request path carries no prefix: it
// is the path that http.StripPrefix or the MountHandler method of the router
// hands over.
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

// serve answers the request from the asset set. upath is the request path with
// the prefix of the asset set already removed.
//
// It writes nothing before it knows which file answers, so a caller is free to
// render a miss any way it likes.
func (a *Assets) serve(w http.ResponseWriter, r *http.Request, upath string) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return errMethod
	}

	name, versioned := a.resolve(upath)
	err := a.write(w, r, name, versioned)
	if !errors.Is(err, errNoFile) || !a.fallback(r, name) {
		return err
	}
	// The index names the versioned assets, so it always revalidates.
	return a.write(w, r, a.index, false)
}

// resolve turns a request path into a file name inside the asset set, and
// reports whether the path carried the build tag.
//
// A path whose first segment is a stale build tag resolves to a file that the
// set does not hold, which answers 404. That is the point of the tag: a client
// that asks for an old build gets told, instead of a new file under an
// immutable URL.
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

// cutSegment removes seg and its slash from the front of name.
func cutSegment(name, seg string) (string, bool) {
	if name == seg {
		return "", true
	}
	return strings.CutPrefix(name, seg+"/")
}

// fallback reports whether the single page application index answers this
// miss. Config.Fallback decides when it is set.
//
// The default answers a navigation only: a path without a file extension, or a
// client that accepts text/html. A missing script keeps its 404, because an
// HTML body in its place breaks the page in a way that is hard to read.
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

// write opens the named file and sends it. A directory answers with the index
// file that it holds, or with the redirect that Config.RedirectDir asks for.
//
// Every failure to open is a miss, whatever the file system reported. A path
// that leaves the asset set fails with an error of its own, and the client
// learns no more from it than it learns from a name that nothing holds.
func (a *Assets) write(w http.ResponseWriter, r *http.Request, name string, versioned bool) error {
	f, info, err := a.openFile(name)
	if err != nil {
		return errNoFile
	}
	//nolint:errcheck // The file is read only.
	defer f.Close()
	if info.IsDir() {
		idx := path.Join(name, a.index)
		// The index has to exist for the slashed form to answer anything. A
		// directory without one keeps the 404 that it answers today, and under
		// SPA it keeps reaching the fallback, which a redirect would take away
		// from it.
		if a.redirectDir && !strings.HasSuffix(r.URL.Path, "/") && a.Has(idx) {
			redirectDir(w, r)
			return nil
		}
		return a.write(w, r, idx, versioned)
	}
	return a.send(w, r, name, f, info, versioned)
}

// redirectDir answers a directory request whose URL carries no trailing slash
// with the 301 that Config.RedirectDir asks for.
//
// The Location is relative: it names the last segment of the request path and
// a slash, so the browser resolves it under whatever prefix the request
// carried, including one that [http.StripPrefix] or the MountHandler method of
// the router removed before this package saw the path. It leads with "./" so
// that a segment which holds a colon does not read as the scheme of an
// absolute URL.
func redirectDir(w http.ResponseWriter, r *http.Request) {
	// EscapedPath keeps the encoding that the client sent, so a name that
	// holds a space or a "#" goes back the way it arrived.
	dest := "./" + path.Base(r.URL.EscapedPath()) + "/"
	if q := r.URL.RawQuery; q != "" {
		dest += "?" + q
	}
	w.Header().Set("Location", dest)
	w.WriteHeader(http.StatusMovedPermanently)
}

// send writes the headers and the body of one file.
func (a *Assets) send(w http.ResponseWriter, r *http.Request, name string, f fs.File, info fs.FileInfo, versioned bool) error {
	// http.ServeContent needs a seeker for a range request and for the size.
	// Every file system that this package opens gives one; a caller that
	// supplies its own may not.
	//
	// The read comes before the headers, so that a failure leaves an
	// untouched response for the caller to render. An immutable Cache-Control
	// on a 500 would let a proxy keep the failure for a year.
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

	// An embedded file reports no modification time. ServeContent then writes
	// no Last-Modified header and answers a conditional request from the ETag.
	http.ServeContent(w, r, path.Base(name), info.ModTime(), rs)
	return nil
}

// openFile opens a file of the asset set and stats it.
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

// etag returns the entity tag of one file.
func (a *Assets) etag(name string, info fs.FileInfo) string {
	if a.etags != nil {
		return a.etags[name]
	}
	// A directory read per request cannot hash the content, so the tag is weak
	// and covers the size and the modification time.
	return `W/"` + strconv.FormatInt(info.Size(), 16) + "-" +
		strconv.FormatInt(info.ModTime().UnixNano(), 16) + `"`
}

// cacheControl returns the Cache-Control header of one answer.
func (a *Assets) cacheControl(name string, versioned bool) string {
	switch {
	// The index names the versioned assets, so it revalidates whatever path
	// reached it. A browser that kept it would keep asking for the assets of
	// the previous build, and a versioned path reaches the index too: through
	// a directory, through the bare build tag, and through URL(Index).
	case path.Base(name) == path.Base(a.index):
		return "no-cache"
	case versioned:
		return immutableCacheControl
	default:
		return a.cache
	}
}
