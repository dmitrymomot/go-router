package static

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/internal/accept"
)

// The precompressed encodings in order of preference, with the file suffix
// that carries each.
var encodings = [...]struct{ name, suffix string }{
	{"br", ".br"},
	{"gzip", ".gz"},
}

// sibling is the precompressed copy of an asset, opened and ready to send.
type sibling struct {
	f        io.ReadSeeker
	closer   io.Closer
	etag     string
	encoding string
	modTime  time.Time
}

func (s *sibling) Close() error { return s.closer.Close() }

// isSibling reports whether name is the precompressed copy of another file of
// the set, which is served through that file and never as an asset of its own.
func (a *Assets) isSibling(name string) bool {
	if a.etags == nil {
		return false
	}
	for _, e := range encodings {
		if base, ok := strings.CutSuffix(name, e.suffix); ok && base != "" {
			if _, ok := a.etags[base]; ok {
				return true
			}
		}
	}
	return false
}

// precompressed opens the sibling that the client accepts, or reports nil. It
// adds Vary: Accept-Encoding whenever the asset has a sibling at all, because
// the answer then depends on the header even for a client that gets the plain
// file.
func (a *Assets) precompressed(w http.ResponseWriter, r *http.Request, name string) *sibling {
	if a.etags == nil {
		return nil
	}
	var offered bool
	for _, e := range encodings {
		if _, ok := a.etags[name+e.suffix]; ok {
			offered = true
			break
		}
	}
	if !offered {
		return nil
	}
	router.AddVary(w.Header(), "Accept-Encoding")

	header := strings.Join(r.Header.Values("Accept-Encoding"), ",")
	for _, e := range encodings {
		if _, ok := a.etags[name+e.suffix]; !ok || accept.EncodingQuality(header, e.name) <= 0 {
			continue
		}
		f, info, err := a.openFile(name + e.suffix)
		if err != nil {
			continue
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok || info.IsDir() {
			_ = f.Close() // The file is read only.
			continue
		}
		tag, _ := strings.CutSuffix(a.etags[name], `"`)
		return &sibling{
			f:        rs,
			closer:   f,
			etag:     tag + "-" + e.suffix[1:] + `"`,
			encoding: e.name,
			modTime:  info.ModTime(),
		}
	}
	return nil
}
