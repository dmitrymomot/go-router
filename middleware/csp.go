package middleware

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
)

// The keywords and schemes of a source list in a [CSPPolicy]. The keywords
// keep their single quotes, which the header needs.
const (
	CSPSelf          = "'self'"
	CSPNone          = "'none'"
	CSPUnsafeInline  = "'unsafe-inline'"
	CSPUnsafeEval    = "'unsafe-eval'"
	CSPStrictDynamic = "'strict-dynamic'"
	CSPData          = "data:"
	CSPBlob          = "blob:"
)

// CSPPolicy is a Content-Security-Policy written as fields, which
// [SecureWithConfig] renders into the header. A directive with an empty list
// is left out, except that SecureConfig.CSPWithNonce adds the nonce of the
// request to ScriptSrc and StyleSrc, empty or not. Each entry is one source,
// such as [CSPSelf] or "https://cdn.example".
//
// ReportURI is the URL of the report-uri directive, and ReportTo the group
// name of the report-to directive, which a Reporting-Endpoints header of your
// own defines. UpgradeInsecureRequests adds the directive of its name.
type CSPPolicy struct {
	DefaultSrc              []string
	ScriptSrc               []string
	StyleSrc                []string
	ImgSrc                  []string
	ConnectSrc              []string
	FontSrc                 []string
	MediaSrc                []string
	ObjectSrc               []string
	FrameSrc                []string
	WorkerSrc               []string
	ManifestSrc             []string
	FrameAncestors          []string
	BaseURI                 []string
	FormAction              []string
	ReportURI               string
	ReportTo                string
	UpgradeInsecureRequests bool
}

// cspNonceSlot stands in for the nonce in a rendered policy until the request
// supplies one. It holds a byte that no source may contain.
const cspNonceSlot = "\x00"

// render checks p and renders it. With nonce, the result holds cspNonceSlot
// where each nonce goes. It panics on a source that would break the header.
func (p *CSPPolicy) render(nonce bool) string {
	var b strings.Builder
	dir := func(name string, srcs []string, withNonce bool) {
		if len(srcs) == 0 && !withNonce {
			return
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(name)
		for _, s := range srcs {
			checkCSPSource(name, s)
			b.WriteByte(' ')
			b.WriteString(s)
		}
		if withNonce {
			b.WriteString(" 'nonce-" + cspNonceSlot + "'")
		}
	}
	dir("default-src", p.DefaultSrc, false)
	dir("script-src", p.ScriptSrc, nonce)
	dir("style-src", p.StyleSrc, nonce)
	dir("img-src", p.ImgSrc, false)
	dir("connect-src", p.ConnectSrc, false)
	dir("font-src", p.FontSrc, false)
	dir("media-src", p.MediaSrc, false)
	dir("object-src", p.ObjectSrc, false)
	dir("frame-src", p.FrameSrc, false)
	dir("worker-src", p.WorkerSrc, false)
	dir("manifest-src", p.ManifestSrc, false)
	dir("frame-ancestors", p.FrameAncestors, false)
	dir("base-uri", p.BaseURI, false)
	dir("form-action", p.FormAction, false)
	if p.UpgradeInsecureRequests {
		// The directive takes no source, which dir would skip.
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString("upgrade-insecure-requests")
	}
	if p.ReportURI != "" {
		dir("report-uri", []string{p.ReportURI}, false)
	}
	if p.ReportTo != "" {
		dir("report-to", []string{p.ReportTo}, false)
	}
	if b.Len() == 0 {
		panic("middleware: SecureConfig.CSP has no directive; set one, such as DefaultSrc: []string{middleware.CSPSelf}")
	}
	return b.String()
}

// checkCSPSource refuses a source that would end its directive or the header
// early, and a keyword written without its quotes, which the browser reads as
// a host name.
func checkCSPSource(directive, s string) {
	if s == "" || strings.ContainsAny(s, " \t\r\n;,\x00") {
		panic("middleware: SecureConfig.CSP has the source " + quoteSource(s) + " in " + directive +
			"; a source is one token, with no space, comma or semicolon")
	}
	switch strings.ToLower(s) {
	case "self", "none", "unsafe-inline", "unsafe-eval", "strict-dynamic":
		panic("middleware: SecureConfig.CSP has the keyword " + s + " in " + directive +
			" without its quotes; write the constant, such as middleware.CSPSelf")
	}
}

func quoteSource(s string) string { return "\"" + strings.ToValidUTF8(s, "?") + "\"" }

// cspNonceKey carries the nonce of [CSPNonce] on the request context.
type cspNonceKey struct{}

// newCSPNonce reports 16 random bytes in unpadded base64url, a nonce of the
// grammar of CSP.
func newCSPNonce() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:]) // crypto/rand never fails on a supported platform.
	return base64.RawURLEncoding.EncodeToString(buf[:])
}

// CSPNonce reports the nonce that [SecureWithConfig] made for the request,
// under SecureConfig.CSPWithNonce, or "" outside such a request. Any context
// of the request works: the handler context c, c.Request().Context(), and the
// ctx a templ component renders with, as in
//
//	<script nonce={ middleware.CSPNonce(ctx) }>...</script>
func CSPNonce(ctx context.Context) string {
	n, _ := ctx.Value(cspNonceKey{}).(string)
	return n
}
