package router

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// The header values of the content types this package writes on every hot
// path. They are shared by all responses and never written to: assigning one
// into the header map skips the one-element slice that Header.Set allocates.
// Their capacity equals their length, so an append copies.
var (
	typeJSON = []string{MIMEApplicationJSONCharsetUTF8}
	typeText = []string{MIMETextPlainCharsetUTF8}
	typeHTML = []string{MIMETextHTMLCharsetUTF8}
)

// typeValue reports the header value for a content type, which is a shared
// slice for the types above.
func typeValue(value string) []string {
	switch value {
	case MIMEApplicationJSONCharsetUTF8:
		return typeJSON
	case MIMETextPlainCharsetUTF8:
		return typeText
	case MIMETextHTMLCharsetUTF8:
		return typeHTML
	}
	return []string{value}
}

func (b *Base) contentType(value string) {
	// An empty value is not a content type. Setting one writes a blank header,
	// and net/http sniffs only when the key is absent, so the response went out
	// with "Content-Type: " and the client had nothing to go on.
	if value == "" {
		return
	}
	h := b.res.Header()
	if cur := h[HeaderContentType]; len(cur) == 0 || cur[0] == "" {
		h[HeaderContentType] = typeValue(value)
	}
}

// validStatus reports whether net/http accepts code in WriteHeader. It panics
// outside 100 to 999.
func validStatus(code int) bool { return code >= 100 && code <= 999 }

func errInvalidStatus(code int) error {
	return ErrInternalServerError.WithError(fmt.Errorf("router: invalid status %d", code))
}

// Blob writes data with contentType and status, and sets Content-Length. A
// status outside 100 to 999 reports an [ErrInternalServerError] and writes
// nothing, as net/http would panic on it; the other writers here do the same.
// An empty contentType leaves the header for net/http to sniff, and a
// Content-Type already set is left alone.
//
// A HEAD request gets the headers and no body. A status that allows no body
// (1xx, 204 and 304) gets neither a body nor a Content-Length.
func (b *Base) Blob(status int, contentType string, data []byte) error {
	if !validStatus(status) {
		return errInvalidStatus(status)
	}
	b.contentType(contentType)
	if !bodyAllowed(status) {
		b.res.WriteHeader(status)
		return nil
	}
	b.res.Header().Set(HeaderContentLength, strconv.Itoa(len(data)))
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead {
		return nil
	}
	_, err := b.res.Write(data)
	return err
}

// String writes s as text/plain with status. It treats HEAD and a status that
// allows no body as [Base.Blob] does.
func (b *Base) String(status int, s string) error {
	if !validStatus(status) {
		return errInvalidStatus(status)
	}
	b.contentType(MIMETextPlainCharsetUTF8)
	if !bodyAllowed(status) {
		b.res.WriteHeader(status)
		return nil
	}
	b.res.Header().Set(HeaderContentLength, strconv.Itoa(len(s)))
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead {
		return nil
	}
	_, err := b.res.WriteString(s)
	return err
}

// bodyAllowed reports whether status may carry a body. net/http refuses a
// write after 1xx, 204 or 304.
func bodyAllowed(status int) bool {
	return status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusNotModified
}

// HTML writes html as text/html with status. The string goes out as it
// stands, so anything built from user input has to be escaped first; see
// [Base.Render] for a template or a component.
func (b *Base) HTML(status int, html string) error {
	if !validStatus(status) {
		return errInvalidStatus(status)
	}
	b.contentType(MIMETextHTMLCharsetUTF8)
	return b.String(status, html)
}

// JSON encodes v and writes it as application/json with status. opts win over
// the options of [Router.JSONOptions]. A value that does not encode reports an
// [ErrInternalServerError] and writes nothing, so the error handler still owns
// the response.
//
// The output follows encoding/json/v2: it does not escape <, > and & as the
// old encoding/json did. That is safe for a JSON body served as
// application/json, and not for one inlined into HTML. To escape them, pass
// jsontext.EscapeForHTML(true) in opts or in [Router.JSONOptions].
func (b *Base) JSON(status int, v any, opts ...json.Options) error {
	buf, err := marshalPooled(v, b.jsonOptions(opts))
	if err != nil {
		return ErrInternalServerError.WithError(fmt.Errorf("router: encode JSON response: %w", err))
	}
	defer putBuf(buf)
	return b.Blob(status, MIMEApplicationJSONCharsetUTF8, buf.Bytes())
}

// marshalPooled encodes v into a pooled buffer, which the caller returns with
// putBuf. On an error the buffer is already returned and nothing was written.
func marshalPooled(v any, opts []json.Options) (*bytes.Buffer, error) {
	buf := getBuf()
	if err := json.MarshalWrite(buf, v, opts...); err != nil {
		putBuf(buf)
		return nil, err
	}
	return buf, nil
}

func (b *Base) jsonOptions(opts []json.Options) []json.Options {
	def := b.opts().jsonOpts
	if len(def) == 0 {
		return opts
	}
	if len(opts) == 0 {
		return def
	}
	out := make([]json.Options, 0, len(def)+len(opts))
	out = append(out, def...)
	return append(out, opts...)
}

// Stream copies r to the response with contentType and status. It sets no
// Content-Length, so the answer is chunked. A HEAD request and a status that
// allows no body leave r unread.
func (b *Base) Stream(status int, contentType string, r io.Reader) error {
	if !validStatus(status) {
		return errInvalidStatus(status)
	}
	b.contentType(contentType)
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead || !bodyAllowed(status) {
		return nil
	}
	_, err := io.Copy(b.res, r)
	return err
}

// NoContent writes status and no body.
func (b *Base) NoContent(status int) error {
	if !validStatus(status) {
		return errInvalidStatus(status)
	}
	b.res.WriteHeader(status)
	return nil
}

// Redirect sets Location and writes status, which has to be one of 300, 301,
// 302, 303, 307 and 308. Any other status reports an
// [ErrInternalServerError] and writes nothing.
func (b *Base) Redirect(status int, location string) error {
	if !isRedirectStatus(status) {
		return ErrInternalServerError.WithError(fmt.Errorf("router: %d is not a redirect status", status))
	}
	b.res.Header().Set(HeaderLocation, location)
	b.res.WriteHeader(status)
	return nil
}

func isRedirectStatus(code int) bool {
	switch code {
	case http.StatusMultipleChoices, http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// Attachment writes data as a download named filename, through a
// Content-Disposition header. See [Base.AttachmentFile] to send a file.
func (b *Base) Attachment(status int, contentType, filename string, data []byte) error {
	if !validStatus(status) {
		return errInvalidStatus(status)
	}
	b.res.Header().Set(HeaderContentDisposition, contentDisposition(dispositionAttachment, filename))
	return b.Blob(status, contentType, data)
}

// Inline writes data for the browser to display rather than save, under
// filename. See [Base.InlineFile] to send a file.
func (b *Base) Inline(status int, contentType, filename string, data []byte) error {
	if !validStatus(status) {
		return errInvalidStatus(status)
	}
	b.res.Header().Set(HeaderContentDisposition, contentDisposition(dispositionInline, filename))
	return b.Blob(status, contentType, data)
}

// Component renders HTML into a writer. A templ component satisfies it as it
// stands; [ComponentFunc] wraps an html/template call.
//
// The context is the [Base] of the request, so [FromContext] recovers it
// inside a component.
type Component interface {
	Render(ctx context.Context, w io.Writer) error
}

// ComponentFunc is a function that renders as a [Component].
type ComponentFunc func(ctx context.Context, w io.Writer) error

// Render calls f.
func (f ComponentFunc) Render(ctx context.Context, w io.Writer) error { return f(ctx, w) }

const renderBufSize = 8 << 10

const maxPooledRenderBuf = 512 << 10

var renderBufs = sync.Pool{
	New: func() any { return bytes.NewBuffer(make([]byte, 0, renderBufSize)) },
}

func keepBuf(buf *bytes.Buffer) bool { return buf.Cap() <= maxPooledRenderBuf }

func getBuf() *bytes.Buffer {
	buf := renderBufs.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// putBuf returns buf to the pool, unless it grew too large to keep.
func putBuf(buf *bytes.Buffer) {
	if keepBuf(buf) {
		buf.Reset()
		renderBufs.Put(buf)
	}
}

func renderError(err error) error {
	if _, ok := errors.AsType[*HTTPError](err); ok {
		return err
	}
	return ErrInternalServerError.WithError(fmt.Errorf("router: render component: %w", err))
}

// Render renders c into a buffer and writes it as text/html with status. The
// buffer means a component that fails halfway writes nothing, so the error
// handler can still answer. See [Base.RenderStream] for a page too large to
// hold.
//
// A render failure that is already an [HTTPError] passes through; any other
// becomes an [ErrInternalServerError].
func (b *Base) Render(status int, c Component) error {
	if !validStatus(status) {
		return errInvalidStatus(status)
	}
	buf := getBuf()
	defer putBuf(buf)

	if err := c.Render(b, buf); err != nil {
		return renderError(err)
	}
	return b.Blob(status, MIMETextHTMLCharsetUTF8, buf.Bytes())
}

// RenderStream renders c straight to the client, which suits a large page and
// a slow one. The header goes out first, so a component that fails halfway
// leaves a truncated body and the error handler cannot answer. A HEAD request
// and a status that allows no body leave c unrendered.
func (b *Base) RenderStream(status int, c Component) error {
	if !validStatus(status) {
		return errInvalidStatus(status)
	}
	b.contentType(MIMETextHTMLCharsetUTF8)
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead || !bodyAllowed(status) {
		return nil
	}
	if err := c.Render(b, b.res); err != nil {
		return renderError(err)
	}
	return nil
}
