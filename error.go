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

type HTTPError struct {
	Status  int
	Message string
	Details any
	Err     error
}

func NewHTTPError(status int, message ...string) *HTTPError {
	e := &HTTPError{Status: status, Message: http.StatusText(status)}
	if len(message) > 0 {
		e.Message = strings.Join(message, " ")
	}
	return e
}

func (e *HTTPError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%d %s: %v", e.Status, e.Message, e.Err)
	}
	return fmt.Sprintf("%d %s", e.Status, e.Message)
}

func (e *HTTPError) Unwrap() error { return e.Err }

func (e *HTTPError) Is(target error) bool {
	t, ok := errors.AsType[*HTTPError](target)
	return ok && t.Status == e.Status
}

func (e *HTTPError) WithMessage(format string, args ...any) *HTTPError {
	c := *e
	if len(args) == 0 {
		c.Message = format
	} else {
		c.Message = fmt.Sprintf(format, args...)
	}
	return &c
}

func (e *HTTPError) WithDetails(details any) *HTTPError {
	c := *e
	c.Details = details
	return &c
}

func (e *HTTPError) WithError(err error) *HTTPError {
	c := *e
	c.Err = err
	return &c
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

type StatusCoder interface {
	error
	StatusCode() int
}

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

const DefaultStackSize = 8 << 10

type PanicValue struct {
	Value any
	Stack []byte
	Err   error
}

func (e *PanicValue) Error() string {
	return fmt.Sprintf("panic: %v\n\n%s", e.Value, e.Stack)
}

func (e *PanicValue) Unwrap() error { return e.Err }

func PanicError(recovered any) *HTTPError {
	return PanicErrorSize(recovered, DefaultStackSize)
}

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

func ResolveStatus(res *Response, err error) int {
	if res != nil && res.Status != 0 {
		return res.Status
	}
	return StatusOf(err)
}

type ErrorHandlerFunc[C Context] func(c C, err error)

type errorBody struct {
	Status  int    `json:"status"`
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
	Cause   string `json:"cause,omitempty"`
}

func DefaultErrorHandler[C Context](c C, err error) {
	writeError(c.base(), err, false)
}

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

var jsonOffers = []string{MIMEApplicationJSON}

// htmx sends "Accept: */*" and swaps what it receives into a page, so it gets
// HTML unless it names JSON itself.
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
		return true
	default:
		return !strings.Contains(accept, "text/")
	}
}
