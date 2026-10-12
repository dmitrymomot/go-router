package static

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
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
		if _, ok := a.etags[name+e.suffix]; !ok || !acceptsEncoding(header, e.name) {
			continue
		}
		f, info, err := a.openFile(name + e.suffix)
		if err != nil {
			continue
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok || info.IsDir() {
			//nolint:errcheck // The file is read only.
			f.Close()
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

// acceptsEncoding reports whether an Accept-Encoding header admits enc. An
// explicit entry wins over "*", and a q of 0 refuses. An encoding the header
// does not mention is not accepted, because the identity file is the safe
// answer.
func acceptsEncoding(header, enc string) bool {
	star := false
	for part := range strings.SplitSeq(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		token = strings.TrimSpace(token)
		q := encodingQuality(params)
		switch {
		case strings.EqualFold(token, enc):
			return q > 0
		case token == "*":
			star = q > 0
		}
	}
	return star
}

func encodingQuality(params string) float64 {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || q < 0 || q > 1 {
			return 0
		}
		return q
	}
	return 1
}
