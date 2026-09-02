package router

import (
	"math"
	"strconv"
	"strings"
)

const (
	HeaderAccept                          = "Accept"
	HeaderAcceptEncoding                  = "Accept-Encoding"
	HeaderAllow                           = "Allow"
	HeaderAuthorization                   = "Authorization"
	HeaderCacheControl                    = "Cache-Control"
	HeaderConnection                      = "Connection"
	HeaderContentDisposition              = "Content-Disposition"
	HeaderContentEncoding                 = "Content-Encoding"
	HeaderContentLength                   = "Content-Length"
	HeaderContentSecurityPolicy           = "Content-Security-Policy"
	HeaderContentSecurityPolicyReportOnly = "Content-Security-Policy-Report-Only"
	HeaderContentType                     = "Content-Type"
	HeaderCookie                          = "Cookie"
	HeaderETag                            = "ETag"
	HeaderForwarded                       = "Forwarded"
	HeaderIfNoneMatch                     = "If-None-Match"
	HeaderLastEventID                     = "Last-Event-Id"
	HeaderLocation                        = "Location"
	HeaderReferer                         = "Referer"
	HeaderReferrerPolicy                  = "Referrer-Policy"
	HeaderRetryAfter                      = "Retry-After"
	HeaderSecFetchSite                    = "Sec-Fetch-Site"
	HeaderStrictTransportSecurity         = "Strict-Transport-Security"
	HeaderUserAgent                       = "User-Agent"
	HeaderVary                            = "Vary"
	HeaderWWWAuthenticate                 = "WWW-Authenticate"
	HeaderXAccelBuffering                 = "X-Accel-Buffering"
	HeaderXCSRFToken                      = "X-CSRF-Token"
	HeaderXContentTypeOptions             = "X-Content-Type-Options"
	HeaderXForwardedFor                   = "X-Forwarded-For"
	HeaderXForwardedProto                 = "X-Forwarded-Proto"
	HeaderXFrameOptions                   = "X-Frame-Options"
	HeaderXRealIP                         = "X-Real-IP"
	HeaderXRequestID                      = "X-Request-Id"

	HeaderAccessControlAllowCredentials = "Access-Control-Allow-Credentials"
	HeaderAccessControlAllowHeaders     = "Access-Control-Allow-Headers"
	HeaderAccessControlAllowMethods     = "Access-Control-Allow-Methods"
	HeaderAccessControlAllowOrigin      = "Access-Control-Allow-Origin"
	HeaderAccessControlExposeHeaders    = "Access-Control-Expose-Headers"
	HeaderAccessControlMaxAge           = "Access-Control-Max-Age"
	HeaderAccessControlRequestHeaders   = "Access-Control-Request-Headers"
	HeaderAccessControlRequestMethod    = "Access-Control-Request-Method"
	HeaderOrigin                        = "Origin"
)

const (
	MIMEApplicationJSON            = "application/json"
	MIMEApplicationJSONCharsetUTF8 = "application/json; charset=utf-8"
	MIMEApplicationForm            = "application/x-www-form-urlencoded"
	MIMEMultipartForm              = "multipart/form-data"
	MIMEOctetStream                = "application/octet-stream"
	MIMETextHTML                   = "text/html"
	MIMETextHTMLCharsetUTF8        = "text/html; charset=utf-8"
	MIMETextPlain                  = "text/plain"
	MIMETextPlainCharsetUTF8       = "text/plain; charset=utf-8"
	MIMETextEventStream            = "text/event-stream"
)

func negotiate(accept string, offers []string) string {
	if len(offers) == 0 {
		return ""
	}
	if strings.TrimSpace(accept) == "" {
		return offers[0]
	}
	best, bestQ := "", 0.0
	for _, offer := range offers {
		if q := acceptQuality(accept, offer); q > bestQ {
			best, bestQ = offer, q
		}
	}
	return best
}

func acceptQuality(accept, offer string) float64 {
	media, _, _ := strings.Cut(offer, ";")
	typ, sub, ok := strings.Cut(strings.TrimSpace(media), "/")
	if !ok {
		return 0
	}
	q, rank := 0.0, -1
	for part := range strings.SplitSeq(accept, ",") {
		rng, params, _ := strings.Cut(part, ";")
		rt, rs, ok := strings.Cut(strings.TrimSpace(rng), "/")
		if !ok {
			continue
		}
		switch r := matchRank(typ, sub, rt, rs); {
		case r > rank:
			rank, q = r, quality(params)
		case r == rank && r >= 0:
			q = max(q, quality(params))
		}
	}
	return q
}

func matchRank(typ, sub, rt, rs string) int {
	switch {
	case rt == "*" && rs == "*":
		return 0
	case rs == "*" && strings.EqualFold(rt, typ):
		return 1
	case strings.EqualFold(rt, typ) && strings.EqualFold(rs, sub):
		return 2
	case strings.EqualFold(rt, typ) && structuredSuffix(rs) == sub:
		// RFC 6839: application/vnd.api+json is JSON with a name on it. A
		// client that asks for one will read the plain offer, and the
		// alternative here is text/plain, which it asked for even less. Bind
		// already reads a +json request body as JSON; this is the same rule on
		// the way out. It ranks below an exact match, so an offer that names
		// the type itself still wins.
		return 1
	default:
		return -1
	}
}

// structuredSuffix is the base syntax of a subtype like "vnd.api+json", or ""
// when it carries no suffix.
func structuredSuffix(sub string) string {
	if i := strings.LastIndexByte(sub, '+'); i >= 0 {
		return sub[i+1:]
	}
	return ""
}

func quality(params string) float64 {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsNaN(q) || math.IsInf(q, 0) || q < 0 || q > 1 {
			return 0
		}
		return q
	}
	return 1
}
