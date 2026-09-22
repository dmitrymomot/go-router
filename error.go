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
//
// Give a domain sentinel such as ErrForbidden of an access package a
// StatusCode method rather than mapping it inside the error handler: the
// error handler, the log of the router, [HTTPErrorOf] and every middleware
// that reads [StatusOf] then agree on its status.
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
// the status of a [StatusCoder], 499 for an error that is [context.Canceled],
// 200 for a nil error, and 500 for anything else. 499 is the status nginx logs
// for a client that went away, so a disconnect does not count as a 5xx.
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
	if errors.Is(err, context.Canceled) {
		return statusClientClosedRequest
	}
	return http.StatusInternalServerError
}

// statusClientClosedRequest has no constant in net/http and no text there.
const statusClientClosedRequest = 499

// ResolveStatus reports the status that went out. A response that already
// wrote its header keeps that status, whatever err asks for; otherwise the
// answer is [StatusOf].
func ResolveStatus(res *Response, err error) int {
	if res != nil && res.Status != 0 {
		return res.Status
	}
	return StatusOf(err)
}

// HTTPErrorOf reports the [HTTPError] the client is answered with: the one
// inside err, or one with the status of [StatusOf], its standard text, and err
// as the cause. A nil err gives nil.
//
// Status is never 0. An empty Message takes the standard text of the status,
// which is itself empty for a status net/http does not name, such as 499.
//
// The result may be err's own HTTPError or a package sentinel, so change it
// only through a With method.
func HTTPErrorOf(err error) *HTTPError {
	if err == nil {
		return nil
	}
	he, ok := errors.AsType[*HTTPError](err)
	if !ok {
		status := StatusOf(err)
		return &HTTPError{Status: status, Message: http.StatusText(status), Err: err}
	}
	if he.Status != 0 && he.Message != "" {
		return he
	}
	// The fields are exported, so a caller can build one with no status or no
	// message. Fix a copy rather than the caller's error.
	c := *he
	if c.Status == 0 {
		c.Status = http.StatusInternalServerError
	}
	if c.Message == "" {
		c.Message = http.StatusText(c.Status)
	}
	return &c
}

// ErrorHandlerFunc writes the answer for a handler that returned an error, and
// returns the error of that write. [Router.ErrorHandler] installs one.
//
// The router calls it at most once per request, and never for a response
// that already committed or for an error that is [context.Canceled]. The
// router logs the failure itself, and answers a bare 500 when the handler
// returns an error having written nothing.
type ErrorHandlerFunc[C Context] func(c C, err error) error

// DefaultErrorHandler writes the status and the message of [HTTPErrorOf] as
// plain text, one line per [FieldError] in its Details. The cause stays out
// of the response.
//
// It is a plain writer: called directly, it neither logs nor checks for a
// committed response. The router does both around every error handler.
func DefaultErrorHandler[C Context](c C, err error) error {
	return writeText(c.base(), err, false)
}

// ErrorHandler is [DefaultErrorHandler] with a say over the cause. With
// exposeCause set, the wrapped cause follows the message in the body, which
// suits a development server and leaks internals anywhere else.
func ErrorHandler[C Context](exposeCause bool) ErrorHandlerFunc[C] {
	return func(c C, err error) error { return writeText(c.base(), err, exposeCause) }
}

func writeText(b *Base, err error, exposeCause bool) error {
	he := HTTPErrorOf(err)
	if he == nil {
		return nil
	}

	// Plain text, always. A handler that wants JSON or HTML for its errors
	// says so in its own ErrorHandler, which knows what its clients read.
	b.res.Header().Set(HeaderContentType, MIMETextPlainCharsetUTF8)
	b.res.WriteHeader(he.Status)
	if b.req.Method == http.MethodHead {
		return nil
	}
	if _, err := b.res.WriteString(he.Message); err != nil {
		return err
	}
	// Field errors say which field failed, which is the useful half of a
	// binding failure, so they get a line each rather than being dropped.
	if fields, ok := he.Details.([]FieldError); ok {
		for _, f := range fields {
			if _, err := b.res.WriteString("\n" + f.Error()); err != nil {
				return err
			}
		}
	}
	if exposeCause && he.Err != nil {
		_, err := b.res.WriteString("\n\n" + he.Err.Error())
		return err
	}
	return nil
}

// HandleError answers err now with the error handler that owns the request,
// so a middleware that calls it after next reads the final Response.Status
// and Size. The router then skips its own call. A nil err, or a call after the
// error was answered, does nothing. Outside a router, on a context from
// [NewBase], it answers with [DefaultErrorHandler].
//
// Like the router, HandleError logs the failure, and skips the error handler
// for a response that already committed and for [context.Canceled].
//
// HandleError panics if c is not the context type of the router that serves
// it, such as the *Base inside an application context.
func HandleError(c Context, err error) {
	if err == nil {
		return
	}
	b := c.base()
	if b.errorHandled {
		return
	}
	if answer := b.opts().answer; answer != nil {
		answer(c, err)
		return
	}
	answerError(c, err, DefaultErrorHandler[Context])
}

// answerError is the error pipeline of a request: the guard, the handler,
// then the log.
func answerError[C Context](c C, err error, h ErrorHandlerFunc[C]) {
	b := c.base()
	// The flag goes up before h runs, so an h that calls HandleError does not
	// recurse. needsCleanup takes a pooled context through the slow clear,
	// which lowers the flag again.
	b.errorHandled, b.needsCleanup = true, true
	committedBefore := b.res.Committed
	if !committedBefore && !errors.Is(err, context.Canceled) {
		runErrorHandler(c, err, h)
	}
	logFailure(b, err, committedBefore)
}

// runErrorHandler runs h and answers a bare 500 when h fails before it wrote
// anything: a panic, or an error returned with nothing written.
func runErrorHandler[C Context](c C, err error, h ErrorHandlerFunc[C]) {
	b := c.base()
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		if rec == http.ErrAbortHandler {
			panic(rec)
		}
		b.Logger().ErrorContext(b.req.Context(), "router: the error handler panicked",
			slog.Any("panic", rec), slog.Any("error", err))
		if !b.res.Committed {
			b.res.WriteHeader(http.StatusInternalServerError)
		}
	}()
	herr := h(c, err)
	switch {
	case herr == nil:
	case b.res.Committed:
		b.Logger().DebugContext(b.req.Context(), "router: the error handler could not finish its answer",
			slog.Any("handler_error", herr), slog.Any("error", err))
	default:
		b.Logger().ErrorContext(b.req.Context(), "router: the error handler failed",
			slog.Any("handler_error", herr), slog.Any("error", err))
		b.res.WriteHeader(http.StatusInternalServerError)
	}
}

// logFailure logs err with the status that went out: the one the error
// handler wrote, or the one err asks for when the response committed before.
// A bare HTTPError under 500 is an answer, not a failure, so it goes unlogged.
func logFailure(b *Base, err error, committedBefore bool) {
	he, isHTTP := errors.AsType[*HTTPError](err)
	var status int
	switch {
	case !committedBefore && b.res.Committed:
		status = b.res.Status
	case isHTTP && he.Status != 0:
		status = he.Status
	default:
		status = StatusOf(err)
	}
	if isHTTP && he.Err == nil && status < http.StatusInternalServerError {
		return
	}

	level := slog.LevelError
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(b.req.Context().Err(), context.Canceled):
		level = slog.LevelDebug
	case status < http.StatusBadRequest:
		// The error handler turned the error into a redirect or a success.
		return
	case status < http.StatusInternalServerError:
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
