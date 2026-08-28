package router

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
)

// HTTPError is an error that carries a status code. A handler returns one to
// select the status that the client sees. Any other error becomes a 500 and
// the message never reaches the client.
type HTTPError struct {
	// Status is the HTTP status code.
	Status int

	// Message is the text that the client sees.
	Message string

	// Details holds an optional payload. The router itself puts a
	// [][FieldError] there, which is what the binder reports, and the default
	// error handler writes it to the response body.
	Details any

	// Err is the internal cause. The default error handler logs it and never
	// writes it to the response.
	Err error
}

// NewHTTPError returns an HTTPError. Without a message it uses the standard
// text of the status code.
func NewHTTPError(status int, message ...string) *HTTPError {
	e := &HTTPError{Status: status, Message: http.StatusText(status)}
	if len(message) > 0 {
		e.Message = strings.Join(message, " ")
	}
	return e
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%d %s: %v", e.Status, e.Message, e.Err)
	}
	return fmt.Sprintf("%d %s", e.Status, e.Message)
}

// Unwrap returns the internal cause.
func (e *HTTPError) Unwrap() error { return e.Err }

// Is reports whether target is an HTTPError with the same status code. It lets
// errors.Is match a returned error against a sentinel such as [ErrNotFound].
func (e *HTTPError) Is(target error) bool {
	t, ok := errors.AsType[*HTTPError](target)
	return ok && t.Status == e.Status
}

// WithMessage returns a copy that carries the message.
func (e *HTTPError) WithMessage(format string, args ...any) *HTTPError {
	c := *e
	if len(args) == 0 {
		c.Message = format
	} else {
		c.Message = fmt.Sprintf(format, args...)
	}
	return &c
}

// WithDetails returns a copy that carries the payload.
func (e *HTTPError) WithDetails(details any) *HTTPError {
	c := *e
	c.Details = details
	return &c
}

// WithError returns a copy that wraps the internal cause.
func (e *HTTPError) WithError(err error) *HTTPError {
	c := *e
	c.Err = err
	return &c
}

// FieldError names one field of a request that did not fit. The binder reports
// a slice of them, and a handler puts one in [HTTPError.Details] to tell the
// client which field to correct:
//
//	return router.ErrUnprocessableEntity.WithDetails([]router.FieldError{
//		{Field: "email", Message: "is not an address"},
//	})
type FieldError struct {
	// Field is the name of the field, as the request spells it.
	Field string `json:"field"`

	// Message is the text that the client sees.
	Message string `json:"message"`
}

// Error implements the error interface, so that a slice of field errors joins
// into one error with [errors.Join].
func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// StatusCoder is an error that names the status of its own answer. A package
// that does not import the router implements it to choose one:
//
//	func (e *NotFoundError) StatusCode() int { return http.StatusNotFound }
//
// [StatusOf] reads it after [HTTPError]. The message of such an error still
// never reaches the client, because only an [HTTPError] carries text that is
// meant for it.
//
// The interface embeds error. [StatusOf] only ever looks for it inside an
// error chain, so every value it can find is an error already, and naming that
// here lets the lookup use errors.AsType.
type StatusCoder interface {
	error
	StatusCode() int
}

// Sentinel errors for the common status codes. Compare a returned error
// against one of them with errors.Is, and build a variant with
// [HTTPError.WithMessage] or [HTTPError.WithError].
var (
	ErrBadRequest           = NewHTTPError(http.StatusBadRequest)
	ErrUnauthorized         = NewHTTPError(http.StatusUnauthorized)
	ErrPaymentRequired      = NewHTTPError(http.StatusPaymentRequired)
	ErrForbidden            = NewHTTPError(http.StatusForbidden)
	ErrNotFound             = NewHTTPError(http.StatusNotFound)
	ErrMethodNotAllowed     = NewHTTPError(http.StatusMethodNotAllowed)
	ErrConflict             = NewHTTPError(http.StatusConflict)
	ErrGone                 = NewHTTPError(http.StatusGone)
	ErrPayloadTooLarge      = NewHTTPError(http.StatusRequestEntityTooLarge)
	ErrUnsupportedMediaType = NewHTTPError(http.StatusUnsupportedMediaType)
	ErrUnprocessableEntity  = NewHTTPError(http.StatusUnprocessableEntity)
	ErrTooManyRequests      = NewHTTPError(http.StatusTooManyRequests)
	ErrInternalServerError  = NewHTTPError(http.StatusInternalServerError)
	ErrNotImplemented       = NewHTTPError(http.StatusNotImplemented)
	ErrBadGateway           = NewHTTPError(http.StatusBadGateway)
	ErrServiceUnavailable   = NewHTTPError(http.StatusServiceUnavailable)
	ErrGatewayTimeout       = NewHTTPError(http.StatusGatewayTimeout)
)

// DefaultStackSize is the stack that [PanicError] keeps. The frames at the top
// name the fault, and a runaway recursion would otherwise write megabytes into
// one log record.
const DefaultStackSize = 8 << 10

// PanicValue carries a recovered panic and the stack of the goroutine that
// raised it. [PanicError] puts one in the internal cause of the error it
// returns, so that an error tracker reads the value and the stack as fields
// instead of cutting them out of a message:
//
//	if pv, ok := errors.AsType[*router.PanicValue](err); ok {
//		tracker.Report(pv.Value, pv.Stack)
//	}
type PanicValue struct {
	// Value is the value that reached panic.
	Value any

	// Stack is the stack of the goroutine that panicked, cut off at the size
	// that the caller asked for.
	Stack []byte

	// Err is Value as an error, which is what Unwrap returns. A panic with a
	// value that is not an error gets one that reads as the value does.
	Err error
}

// Error implements the error interface. It reads as the report that the
// runtime prints for a panic that nothing recovered.
func (e *PanicValue) Error() string {
	return fmt.Sprintf("panic: %v\n\n%s", e.Value, e.Stack)
}

// Unwrap returns the panic value as an error, so that errors.Is reaches an
// error that a handler passed to panic.
func (e *PanicValue) Unwrap() error { return e.Err }

// PanicError turns a recovered panic value into a 500 error whose internal
// cause is a [PanicValue] carrying the panic and the stack. The router calls it
// for a panic that escapes the handler chain, and [middleware.Recover] calls it
// for one that it catches inside the chain.
//
// The stack reaches the error handler, which logs it. It never reaches the
// client.
func PanicError(recovered any) *HTTPError {
	return PanicErrorSize(recovered, DefaultStackSize)
}

// PanicErrorSize is [PanicError] with the size of the stack buffer, which a
// middleware takes from its config. A size of zero or less uses
// [DefaultStackSize].
func PanicErrorSize(recovered any, stackSize int) *HTTPError {
	if stackSize <= 0 {
		stackSize = DefaultStackSize
	}
	buf := make([]byte, stackSize)
	// The panicking goroutine is the one that matters, and the stacks of every
	// other request would bury it.
	n := runtime.Stack(buf, false)

	cause, ok := recovered.(error)
	if !ok {
		cause = errors.New(fmt.Sprint(recovered))
	}
	return ErrInternalServerError.WithError(&PanicValue{
		Value: recovered,
		Stack: buf[:n],
		Err:   cause,
	})
}

// StatusOf returns the status code that [DefaultErrorHandler] writes for err:
// the status of an [HTTPError], the code of a [StatusCoder], 500 for any other
// error, and 200 for nil.
//
// Middleware uses it to report the status of a request whose handler returned
// an error, because the error handler runs after the middleware chain unwinds
// and the response is still uncommitted at that point.
func StatusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if he, ok := errors.AsType[*HTTPError](err); ok {
		return he.Status
	}
	if sc, ok := errors.AsType[StatusCoder](err); ok {
		if status := sc.StatusCode(); status != 0 {
			return status
		}
	}
	return http.StatusInternalServerError
}

// ResolveStatus reports the status the client sees: the committed status when
// the handler already wrote one, otherwise the status that err produces.
//
// A logging or a metrics middleware needs both halves, because the error
// handler runs after the middleware chain unwinds and the response is still
// uncommitted while the middleware reads it:
//
//	err := next(c)
//	status := router.ResolveStatus(c.Response(), err)
func ResolveStatus(res *Response, err error) int {
	if res != nil && res.Status != 0 {
		return res.Status
	}
	return StatusOf(err)
}

// ErrorHandlerFunc renders an error that a handler or a middleware returned.
// Set one on the router with [Router.ErrorHandler].
type ErrorHandlerFunc[C Context] func(c C, err error)

// errorBody is the JSON shape that [DefaultErrorHandler] writes. Cause comes
// last, because the fields above it are the documented wire shape and only
// [ErrorHandler] with exposeCause fills it.
type errorBody struct {
	Status  int    `json:"status"`
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
	Cause   string `json:"cause,omitempty"`
}

// DefaultErrorHandler writes the error to the response. It reads the status
// and the message from an [HTTPError], and reports any other error with the
// standard text of the status that [StatusOf] gives it, so an internal message
// never reaches the client. It logs the internal cause with the logger of the
// router: at error level for a 5xx, at warning level for a 4xx, which is the
// client and not the server, and at debug level when the client cancelled the
// request.
//
// It writes JSON unless the client asked for text. A client that sends no
// Accept header gets JSON, because a service that calls another service
// usually sends none and still wants a machine readable answer.
//
// An htmx request is the exception: it gets the message as HTML, with its
// markup escaped, because htmx swaps what it receives into a page. One that
// names JSON in its Accept header still gets JSON. The answer therefore varies
// on HX-Request, which it adds to the Vary header.
func DefaultErrorHandler[C Context](c C, err error) {
	writeError(c.base(), err, false)
}

// ErrorHandler returns an error handler that also writes the internal cause
// into the body. Use it in development only:
//
//	if dev {
//		r.ErrorHandler(router.ErrorHandler[*app.Context](true))
//	}
//
// With exposeCause false it is [DefaultErrorHandler], down to the bytes.
func ErrorHandler[C Context](exposeCause bool) ErrorHandlerFunc[C] {
	return func(c C, err error) { writeError(c.base(), err, exposeCause) }
}

// writeError is the one implementation behind [DefaultErrorHandler] and
// [ErrorHandler].
func writeError(b *Base, err error, exposeCause bool) {
	if err == nil {
		return
	}

	he, ok := errors.AsType[*HTTPError](err)
	if !ok {
		// A StatusCoder names the status. Its message stays internal all the
		// same, so the client reads the standard text of that status.
		he = NewHTTPError(StatusOf(err)).WithError(err)
	}

	if he.Status >= http.StatusInternalServerError || he.Err != nil {
		logFailure(b, err, he.Status)
	}
	if !writableFailure(b, err, he.Status) {
		return
	}

	cause := ""
	if exposeCause && he.Err != nil {
		cause = he.Err.Error()
	}

	// The body and its media type both depend on HX-Request, so a shared cache
	// has to keep the two answers apart. Without it a cache that stores error
	// responses hands an htmx fragment to an API client, or a JSON object to a
	// page.
	b.Vary(HeaderHXRequest)

	if acceptsJSON(b.req) {
		b.res.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
		b.res.WriteHeader(he.Status)
		//nolint:errcheck // The connection is already failing; nothing to report.
		json.MarshalWrite(b.res, errorBody{
			Status: he.Status, Error: he.Message, Details: he.Details, Cause: cause,
		})
		return
	}

	// An htmx page may swap this body into itself, so the message goes out as
	// HTML with its markup escaped. htmx swaps no 4xx or 5xx by default, but
	// a page that configures responseHandling, or that uses the
	// response-targets extension, does.
	if b.IsHTMX() {
		b.res.Header().Set(HeaderContentType, MIMETextHTMLCharsetUTF8)
		b.res.WriteHeader(he.Status)
		//nolint:errcheck // Same as above.
		b.res.WriteString(html.EscapeString(he.Message))
		return
	}

	b.res.Header().Set(HeaderContentType, MIMETextPlainCharsetUTF8)
	b.res.WriteHeader(he.Status)
	//nolint:errcheck // Same as above.
	b.res.WriteString(he.Message)
	if cause != "" {
		//nolint:errcheck // Same as above.
		b.res.WriteString("\n\n" + cause)
	}
}

// logFailure logs the internal cause of a failed request. Every error handler
// of this package reports a failure through it, so the record reads the same
// whichever one renders the answer.
//
// The status picks the level, the way [middleware.LoggerConfig] does: a 5xx is
// a server fault and reads at error level, and a 4xx is a client that sent
// something the server refused and reads at warning level. A malformed body,
// an unreadable form field and a body over the limit are all ordinary traffic,
// and a client that repeats one would otherwise fill the error stream and every
// alert keyed on it.
//
// A client that went away is not a fault at all. A long streamed page fails
// that way whenever a reader closes the tab, so report it at debug level.
func logFailure(b *Base, err error, status int) {
	level := slog.LevelError
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(b.req.Context().Err(), context.Canceled):
		level = slog.LevelDebug
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
		level = slog.LevelWarn
	}
	b.Logger().Log(b.req.Context(), level, "router: request failed",
		slog.String("method", b.req.Method),
		slog.String("path", b.req.URL.Path),
		slog.String("route", b.pattern),
		slog.Int("status", status),
		slog.Any("error", err),
	)
}

// writableFailure reports whether an error handler still has a body to write.
//
// A committed response and a client that went away leave nothing to answer. A
// HEAD request takes the status alone, which this writes before it says no.
func writableFailure(b *Base, err error, status int) bool {
	if b.res.Committed || errors.Is(err, context.Canceled) {
		return false
	}
	if b.req.Method == http.MethodHead {
		b.res.WriteHeader(status)
		return false
	}
	return true
}

// jsonOffers is the offer list that acceptsJSON negotiates with.
var jsonOffers = []string{MIMEApplicationJSON}

// acceptsJSON reports whether the client takes a JSON body. It answers yes
// unless the client named the types it takes and every one of them is a text
// type.
//
// It is more forgiving than [Base.Accepts] on purpose, because the answer it
// governs is an error and every client is better off with one it can read.
//
// htmx is the exception. It sends "Accept: */*" and swaps what it receives
// into a page, so a JSON error would land there as text. A client that names
// JSON still gets JSON, which is what an htmx page that sets its own Accept
// header asked for.
func acceptsJSON(r *http.Request) bool {
	accept := r.Header.Get(HeaderAccept)
	if strings.Contains(accept, "json") {
		return true
	}
	if hxTrue(r.Header.Get(HeaderHXRequest)) {
		return false
	}
	switch {
	case accept == "", negotiate(accept, jsonOffers) != "":
		return true
	case strings.Contains(accept, "*/*"):
		// A client that takes any type at all takes the JSON body. A "+json"
		// flavour that no offer fits already answered above.
		return true
	default:
		// The client named the types it takes, so it gets JSON unless every one
		// of them is a text type.
		return !strings.Contains(accept, "text/")
	}
}
