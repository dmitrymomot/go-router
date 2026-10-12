package router

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
)

// ETagOf reports a strong entity tag for data: the quoted, URL-safe base64 of
// the first 16 bytes of its SHA-256. Equal bytes give equal tags.
func ETagOf(data []byte) string {
	sum := sha256.Sum256(data)
	var buf [24]byte
	buf[0] = '"'
	base64.RawURLEncoding.Encode(buf[1:23], sum[:16])
	buf[23] = '"'
	return string(buf[:])
}

// CheckETag sets the ETag header to etag and, for a GET or HEAD request,
// compares it with If-None-Match. When the request already holds that entity
// it writes 304 Not Modified and reports true, and the handler returns nil
// without writing a body. Otherwise it reports false and the handler writes
// the response. Other methods only get the header.
//
// etag is a quoted entity tag, such as the result of [ETagOf]. The comparison
// is weak, as RFC 9110 asks for If-None-Match: a "W/" prefix on either side is
// ignored, "*" matches anything, and a comma-separated list matches when any
// member does. Call it after the headers that belong on a 304 (Cache-Control,
// Vary) and before the body.
func (b *Base) CheckETag(etag string) bool {
	h := b.res.Header()
	h.Set(HeaderETag, etag)
	if b.req.Method != http.MethodGet && b.req.Method != http.MethodHead {
		return false
	}
	if !etagMatches(b.req.Header[HeaderIfNoneMatch], etag) {
		return false
	}
	// A 304 carries no body, so the headers that describe one go.
	delete(h, HeaderContentLength)
	delete(h, HeaderContentType)
	b.res.WriteHeader(http.StatusNotModified)
	return true
}

// etagMatches reports whether any entity tag in the If-None-Match values
// weakly matches etag.
func etagMatches(values []string, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for _, v := range values {
		for {
			v = strings.TrimLeft(v, " \t,")
			if v == "" {
				break
			}
			if v[0] == '*' {
				return true
			}
			v = strings.TrimPrefix(v, "W/")
			if v == "" || v[0] != '"' {
				break
			}
			end := strings.IndexByte(v[1:], '"')
			if end < 0 {
				break
			}
			if v[:end+2] == want {
				return true
			}
			v = v[end+2:]
		}
	}
	return false
}
