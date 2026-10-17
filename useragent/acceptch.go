package useragent

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// The Client Hints that [ParseHeaders] reads. A Chromium browser sends the
// three low-entropy ones on every request, and the high-entropy ones only to
// an origin that asked for them in Accept-CH.
const (
	HeaderSecCHUA                = "Sec-CH-UA"
	HeaderSecCHUAMobile          = "Sec-CH-UA-Mobile"
	HeaderSecCHUAPlatform        = "Sec-CH-UA-Platform"
	HeaderSecCHUAPlatformVersion = "Sec-CH-UA-Platform-Version"
	HeaderSecCHUAFullVersionList = "Sec-CH-UA-Full-Version-List"
	HeaderSecCHUAModel           = "Sec-CH-UA-Model"
)

// The keys of the response headers in the canonical form of net/http, which a
// direct map write needs.
var (
	headerAcceptCH   = http.CanonicalHeaderKey("Accept-CH")
	headerCriticalCH = http.CanonicalHeaderKey("Critical-CH")
)

// DefaultClientHints is the list [AcceptCH] asks for when
// AcceptCHConfig.Hints is nil: the high-entropy hints that [ParseHeaders]
// reads, which give the real version of the browser and of Windows or macOS,
// and the model of a phone.
var DefaultClientHints = []string{HeaderSecCHUAPlatformVersion, HeaderSecCHUAFullVersionList, HeaderSecCHUAModel}

// AcceptCHConfig configures [AcceptCHWithConfig]. Hints lists the Client
// Hints to ask for; a nil list takes [DefaultClientHints]. Critical also
// sends them in Critical-CH, so a browser that lacks them retries the first
// navigation with them, at the cost of one more round trip; without it they
// arrive from the second request on. A request for which Skip reports true
// gets no header.
type AcceptCHConfig[C router.Context] struct {
	Skip     func(c C) bool
	Hints    []string
	Critical bool
}

// AcceptCH asks the browser for the high-entropy Client Hints that [Of] reads,
// through the Accept-CH header of every answer, and adds them to Vary, since
// an answer built from them differs by them. A browser sends hints only over
// HTTPS, and only to the origin that asked.
func AcceptCH[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return AcceptCHWithConfig(AcceptCHConfig[C]{})(next)
}

// AcceptCHWithConfig is [AcceptCH] with a configuration.
//
// AcceptCHWithConfig panics on an empty Hints that is not nil, and on a hint
// that is not one header name, such as "Sec-CH-UA-Model, DPR" in one entry.
func AcceptCHWithConfig[C router.Context](cfg AcceptCHConfig[C]) router.Middleware[C] {
	hints := cfg.Hints
	if hints == nil {
		hints = DefaultClientHints
	}
	if len(hints) == 0 {
		panic("useragent: AcceptCHConfig.Hints is empty; leave it nil for DefaultClientHints")
	}
	for i, h := range hints {
		if h == "" || strings.ContainsFunc(h, func(r rune) bool { return !isTokenRune(r) }) {
			panic(fmt.Sprintf("useragent: AcceptCHConfig.Hints[%d] is %q, which is not one header name", i, h))
		}
	}
	hints = append([]string(nil), hints...)
	value := []string{strings.Join(hints, ", ")}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}
			h := c.Response().Header()
			// A shared slice of capacity one: a later Add copies it rather
			// than writing into another response's header.
			h[headerAcceptCH] = value[:1:1]
			if cfg.Critical {
				h[headerCriticalCH] = value[:1:1]
			}
			router.AddVary(h, hints...)
			return next(c)
		}
	}
}

// isTokenRune reports the characters of an RFC 9110 token.
func isTokenRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}
