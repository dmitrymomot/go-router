package router

import (
	"strings"

	"github.com/dmitrymomot/go-router/internal/accept"
)

// The header names that this package and its middleware read or write. Most
// are in the canonical form of net/http, so they suit a direct map read.
// HeaderETag, HeaderWWWAuthenticate, HeaderXCSRFToken and HeaderXRealIP keep
// the spelling of their RFCs instead, which net/http stores as "Etag",
// "Www-Authenticate", "X-Csrf-Token" and "X-Real-Ip": reach those through the
// Get, Set, Values and Del methods of [http.Header], which canonicalize.
const (
	HeaderAccept                          = "Accept"
	HeaderAcceptEncoding                  = "Accept-Encoding"
	HeaderAcceptLanguage                  = "Accept-Language"
	HeaderAllow                           = "Allow"
	HeaderAuthorization                   = "Authorization"
	HeaderCacheControl                    = "Cache-Control"
	HeaderConnection                      = "Connection"
	HeaderContentDisposition              = "Content-Disposition"
	HeaderContentEncoding                 = "Content-Encoding"
	HeaderContentLanguage                 = "Content-Language"
	HeaderContentLength                   = "Content-Length"
	HeaderContentSecurityPolicy           = "Content-Security-Policy"
	HeaderContentSecurityPolicyReportOnly = "Content-Security-Policy-Report-Only"
	HeaderContentType                     = "Content-Type"
	HeaderCookie                          = "Cookie"
	HeaderETag                            = "ETag"
	HeaderForwarded                       = "Forwarded"
	HeaderIdempotencyKey                  = "Idempotency-Key"
	HeaderIfNoneMatch                     = "If-None-Match"
	HeaderLastModified                    = "Last-Modified"
	HeaderLocation                        = "Location"
	HeaderReferer                         = "Referer"
	HeaderReferrerPolicy                  = "Referrer-Policy"
	HeaderRetryAfter                      = "Retry-After"
	HeaderSecFetchSite                    = "Sec-Fetch-Site"
	HeaderSetCookie                       = "Set-Cookie"
	HeaderStrictTransportSecurity         = "Strict-Transport-Security"
	HeaderUserAgent                       = "User-Agent"
	HeaderVary                            = "Vary"
	HeaderWWWAuthenticate                 = "WWW-Authenticate"
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

// The media types that this package writes or recognizes.
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

func acceptQuality(header, offer string) float64 {
	_, q := accept.Match(header, offer)
	return q
}
