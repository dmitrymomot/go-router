package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
)

// HTTPError is an error that names the status the client sees. A handler
// returns one, and the error handler of the router writes it.
//
// Build one from a sentinel such as [ErrNotFound] rather than from scratch:
// the With methods each copy the receiver, so a package-level sentinel stays
// untouched.
type HTTPError struct {
	Status  int
	Message string
	Details any
	Err     error
}

// NewHTTPError builds an error for status. Without a message it takes the
// standard text of the status; several message parts join with a space.
func NewHTTPError(status int, message ...string) *HTTPError {
	e := &HTTPError{Status: status, Message: http.StatusText(status)}
	if len(message) > 0 {
		e.Message = strings.Join(message, " ")
	}
	return e
}

// Error reports the status, the message, and the wrapped cause when there is
// one.
func (e *HTTPError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%d %s: %v", e.Status, e.Message, e.Err)
	}
	return fmt.Sprintf("%d %s", e.Status, e.Message)
}

// Unwrap reports the wrapped cause, which [HTTPError.WithError] sets.
func (e *HTTPError) Unwrap() error { return e.Err }

// Is reports whether target is an HTTPError of the same status, so
// errors.Is(err, [ErrNotFound]) matches any 404 this package builds.
func (e *HTTPError) Is(target error) bool {
	t, ok := errors.AsType[*HTTPError](target)
	return ok && t.Status == e.Status
}

// WithMessage copies e with the message that format and args build. Without
// args, format is the message as it stands.
func (e *HTTPError) WithMessage(format string, args ...any) *HTTPError {
	c := *e
	if len(args) == 0 {
		c.Message = format
	} else {
		c.Message = fmt.Sprintf(format, args...)
	}
	return &c
}

// WithDetails copies e with details attached. The default error handler writes
// a []FieldError one line per field, and leaves any other type to a handler
// that knows it.
func (e *HTTPError) WithDetails(details any) *HTTPError {
	c := *e
	c.Details = details
	return &c
}

// WithError copies e with err as its cause. The cause reaches the log, and it
// reaches the client only through [ErrorHandler] with exposeCause set.
func (e *HTTPError) WithError(err error) *HTTPError {
	c := *e
	c.Err = err
	return &c
}

// FieldError names one field that failed validation. [Base.Bind] collects
// them into the Details of an [ErrUnprocessableEntity].
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error reports the field and its message.
func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// StatusCoder is an error of your own that names its status. [StatusOf] reads
// it, so a domain error reaches the client with the right status without
// being wrapped in an [HTTPError].
type StatusCoder interface {
	error
	StatusCode() int
}

// The sentinel for each status this package builds. Return one as it stands,
// or copy it with [HTTPError.WithMessage], [HTTPError.WithDetails] or
// [HTTPError.WithError]. errors.Is matches on the status alone.
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

// DefaultStackSize is how many bytes of stack [PanicError] records.
const DefaultStackSize = 8 << 10

// PanicValue is the value a handler panicked with, and the stack at that
// moment. [PanicError] wraps one in an [ErrInternalServerError], so the stack
// reaches the log and never the client.
type PanicValue struct {
	Value any
	Stack []byte
	Err   error
}

// Error reports the value and the stack.
func (e *PanicValue) Error() string {
	return fmt.Sprintf("panic: %v\n\n%s", e.Value, e.Stack)
}

// Unwrap reports the panic value as an error. A panic with a value that is not
// an error is wrapped in one built from its printed form.
func (e *PanicValue) Unwrap() error { return e.Err }

// PanicError turns the result of recover into an [ErrInternalServerError] that
// carries a [PanicValue]. It records [DefaultStackSize] bytes of stack.
func PanicError(recovered any) *HTTPError {
	return PanicErrorSize(recovered, DefaultStackSize)
}

// PanicErrorSize is [PanicError] with the size of the stack it records. A
// stackSize of zero or less takes [DefaultStackSize].
func PanicErrorSize(recovered any, stackSize int) *HTTPError {
	if stackSize <= 0 {
		stackSize = DefaultStackSize
	}
	buf := make([]byte, stackSize)
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

// StatusOf reports the status that err asks for: the status of an [HTTPError],
// the status of a [StatusCoder], 200 for a nil error, and 500 for anything
// else.
func StatusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if he, ok := errors.AsType[*HTTPError](err); ok {
		// The fields are exported, so a caller can build one with no status.
		if he.Status != 0 {
			return he.Status
		}
		return http.StatusInternalServerError
	}
	if sc, ok := errors.AsType[StatusCoder](err); ok {
		if status := sc.StatusCode(); status != 0 {
			return status
		}
	}
	return http.StatusInternalServerError
}

// ResolveStatus reports the status that went out. A response that already
// wrote its header keeps that status, whatever err asks for; otherwise the
// answer is [StatusOf].
func ResolveStatus(res *Response, err error) int {
	if res != nil && res.Status != 0 {
		return res.Status
	}
	return StatusOf(err)
}

// ErrorHandlerFunc writes the answer for a handler that returned an error.
// [Router.ErrorHandler] installs one, and the router calls it once per failed
// request.
type ErrorHandlerFunc[C Context] func(c C, err error)

// DefaultErrorHandler writes the status and the message of err as plain text,
// one line per [FieldError] in its Details. It logs any 5xx and any error that
// carries a cause, and it keeps the cause out of the response.
//
// A response that already committed is logged and not written again.
func DefaultErrorHandler[C Context](c C, err error) {
	writeError(c.base(), err, false)
}

// ErrorHandler is [DefaultErrorHandler] with a say over the cause. With
// exposeCause set, the wrapped cause follows the message in the body, which
// suits a development server and leaks internals anywhere else.
func ErrorHandler[C Context](exposeCause bool) ErrorHandlerFunc[C] {
	return func(c C, err error) { writeError(c.base(), err, exposeCause) }
}

func writeError(b *Base, err error, exposeCause bool) {
	if err == nil {
		return
	}

	he, ok := errors.AsType[*HTTPError](err)
	if !ok {
		he = NewHTTPError(StatusOf(err)).WithError(err)
	}

	// The fields are exported, so a caller can build one with no status, and
	// WriteHeader panics on 0. Read it here rather than mutating the caller's
	// error.
	status := he.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}

	if status >= http.StatusInternalServerError || he.Err != nil {
		logFailure(b, err, status)
	}
	if !writableFailure(b, err) {
		return
	}

	cause := ""
	if exposeCause && he.Err != nil {
		cause = he.Err.Error()
	}

	// Plain text, always. A handler that wants JSON or HTML for its errors
	// says so in its own ErrorHandler, which knows what its clients read.
	b.res.Header().Set(HeaderContentType, MIMETextPlainCharsetUTF8)
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead {
		return
	}
	//nolint:errcheck // The connection is already failing; nothing to report.
	b.res.WriteString(he.Message)
	// Field errors say which field failed, which is the useful half of a
	// binding failure, so they get a line each rather than being dropped.
	if fields, ok := he.Details.([]FieldError); ok {
		for _, f := range fields {
			//nolint:errcheck // Same as above.
			b.res.WriteString("\n" + f.Error())
		}
	}
	if cause != "" {
		//nolint:errcheck // Same as above.
		b.res.WriteString("\n\n" + cause)
	}
}

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

func writableFailure(b *Base, err error) bool {
	if b.res.Committed || errors.Is(err, context.Canceled) {
		return false
	}
	return true
}
