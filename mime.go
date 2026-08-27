package router

// Header names that the router and the middleware set or read.
const (
	HeaderAccept             = "Accept"
	HeaderAllow              = "Allow"
	HeaderAuthorization      = "Authorization"
	HeaderCacheControl       = "Cache-Control"
	HeaderConnection         = "Connection"
	HeaderContentDisposition = "Content-Disposition"
	HeaderContentLength      = "Content-Length"
	HeaderContentType        = "Content-Type"
	HeaderLastEventID        = "Last-Event-Id"
	HeaderLocation           = "Location"
	HeaderRetryAfter         = "Retry-After"
	HeaderVary               = "Vary"
	HeaderXAccelBuffering    = "X-Accel-Buffering"
	HeaderXForwardedFor      = "X-Forwarded-For"
	HeaderXForwardedProto    = "X-Forwarded-Proto"
	HeaderXRealIP            = "X-Real-IP"
	HeaderXRequestID         = "X-Request-Id"

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

// Media types that the render helpers write.
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
