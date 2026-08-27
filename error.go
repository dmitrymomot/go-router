package router

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
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

	// Details holds an optional payload, such as a map of field errors. The
	// default error handler writes it to the response body.
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

// PanicError turns a recovered panic value into a 500 error whose internal
// cause carries the panic and the stack. The router calls it for a panic that
// escapes the handler chain, and [middleware.Recover] calls it for one that it
// catches inside the chain.
//
// The stack reaches the error handler, which logs it. It never reaches the
// client.
func PanicError(recovered any) *HTTPError {
	cause, ok := recovered.(error)
	if !ok {
		cause = fmt.Errorf("%v", recovered)
	}
	return ErrInternalServerError.WithError(
		fmt.Errorf("panic: %w\n\n%s", cause, debug.Stack()))
}

// StatusOf returns the status code that [DefaultErrorHandler] writes for err:
// the status of an [HTTPError], 500 for any other error, and 200 for nil.
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
	return http.StatusInternalServerError
}

// ErrorHandlerFunc renders an error that a handler or a middleware returned.
// Set one on the router with [Router.ErrorHandler].
type ErrorHandlerFunc[C Context] func(c C, err error)

// errorBody is the JSON shape that [DefaultErrorHandler] writes.
type errorBody struct {
	Status  int    `json:"status"`
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
}

// DefaultErrorHandler writes the error to the response. It reads the status
// and the message from an [HTTPError] and reports any other error as a 500
// with the standard status text, so an internal message never reaches the
// client. It logs the internal cause with [slog.Default], at error level, or
// at debug level when the client cancelled the request.
//
// It writes JSON unless the client asked for text. A client that sends no
// Accept header gets JSON, because a service that calls another service
// usually sends none and still wants a machine readable answer.
func DefaultErrorHandler[C Context](c C, err error) {
	if err == nil {
		return
	}
	b := c.base()

	he, ok := errors.AsType[*HTTPError](err)
	if !ok {
		he = ErrInternalServerError.WithError(err)
	}

	if he.Status >= http.StatusInternalServerError || he.Err != nil {
		// A client that went away is not a server fault. A long streamed page
		// fails that way whenever a reader closes the tab, so report it at
		// debug level and keep it out of the 5xx alerts.
		level := slog.LevelError
		if errors.Is(err, context.Canceled) || errors.Is(b.req.Context().Err(), context.Canceled) {
			level = slog.LevelDebug
		}
		slog.Log(b.req.Context(), level, "router: request failed",
			slog.String("method", b.req.Method),
			slog.String("path", b.req.URL.Path),
			slog.String("route", b.pattern),
			slog.Int("status", he.Status),
			slog.Any("error", err),
		)
	}

	// The client is gone, or the handler already wrote the response.
	if b.res.Committed || errors.Is(err, context.Canceled) {
		return
	}

	if b.req.Method == http.MethodHead {
		b.res.WriteHeader(he.Status)
		return
	}

	if acceptsJSON(b.req) {
		b.res.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
		b.res.WriteHeader(he.Status)
		//nolint:errcheck // The connection is already failing; nothing to report.
		json.MarshalWrite(b.res, errorBody{Status: he.Status, Error: he.Message, Details: he.Details})
		return
	}

	b.res.Header().Set(HeaderContentType, MIMETextPlainCharsetUTF8)
	b.res.WriteHeader(he.Status)
	//nolint:errcheck // Same as above.
	b.res.WriteString(he.Message)
}

// acceptsJSON reports whether the client takes a JSON body. It answers yes
// unless the client named the types it takes and every one of them is a text
// type.
func acceptsJSON(r *http.Request) bool {
	accept := r.Header.Get(HeaderAccept)
	switch {
	case accept == "",
		strings.Contains(accept, "json"),
		strings.Contains(accept, "*/*"):
		return true
	default:
		return !strings.Contains(accept, "text/")
	}
}
