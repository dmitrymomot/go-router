package router

import (
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
	HeaderXHTTPMethodOverride             = "X-HTTP-Method-Override"
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
	MIMEApplicationProblemJSON     = "application/problem+json"
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
	default:
		return -1
	}
}

func quality(params string) float64 {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || q < 0 {
			return 0
		}
		return min(q, 1)
	}
	return 1
}
