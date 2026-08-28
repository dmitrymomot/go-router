package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// contentType sets the Content-Type header unless the handler already set one.
func (b *Base) contentType(value string) {
	h := b.res.Header()
	if h.Get(HeaderContentType) == "" {
		h.Set(HeaderContentType, value)
	}
}

// Blob writes raw bytes with the given media type.
func (b *Base) Blob(status int, contentType string, data []byte) error {
	b.contentType(contentType)
	b.res.Header().Set(HeaderContentLength, strconv.Itoa(len(data)))
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead {
		return nil
	}
	_, err := b.res.Write(data)
	return err
}

// String writes a plain text body.
func (b *Base) String(status int, s string) error {
	b.contentType(MIMETextPlainCharsetUTF8)
	b.res.Header().Set(HeaderContentLength, strconv.Itoa(len(s)))
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead {
		return nil
	}
	_, err := b.res.WriteString(s)
	return err
}

// Stringf writes a formatted plain text body.
func (b *Base) Stringf(status int, format string, args ...any) error {
	return b.String(status, fmt.Sprintf(format, args...))
}

// HTML writes an HTML body.
func (b *Base) HTML(status int, html string) error {
	b.contentType(MIMETextHTMLCharsetUTF8)
	return b.String(status, html)
}

// JSON writes v as JSON. It encodes the whole value before it writes the
// header, so a value that cannot be encoded produces a clean 500 instead of a
// truncated body.
//
// It uses encoding/json/v2, which rejects invalid UTF-8 and duplicate object
// names.
func (b *Base) JSON(status int, v any, opts ...json.Options) error {
	data, err := json.Marshal(v, b.jsonOptions(opts)...)
	if err != nil {
		return ErrInternalServerError.WithError(fmt.Errorf("router: encode JSON response: %w", err))
	}
	return b.Blob(status, MIMEApplicationJSONCharsetUTF8, data)
}

// jsonOptions puts the options of the router in front of the per-call ones, so
// that a call can override a default.
func (b *Base) jsonOptions(opts []json.Options) []json.Options {
	def := b.opts().jsonOpts
	if len(def) == 0 {
		return opts
	}
	out := make([]json.Options, 0, len(def)+len(opts))
	out = append(out, def...)
	return append(out, opts...)
}

// JSONPretty writes v as indented JSON.
func (b *Base) JSONPretty(status int, v any, indent string) error {
	return b.JSON(status, v, jsontext.Multiline(true), jsontext.WithIndent(indent))
}

// Stream copies a reader to the response body.
func (b *Base) Stream(status int, contentType string, r io.Reader) error {
	b.contentType(contentType)
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead {
		return nil
	}
	_, err := io.Copy(b.res, r)
	return err
}

// NoContent writes only the status line.
func (b *Base) NoContent(status int) error {
	b.res.WriteHeader(status)
	return nil
}

// Redirect answers with a redirect status and a Location header. It reports an
// error when the status is not a 3xx code.
func (b *Base) Redirect(status int, location string) error {
	if status < http.StatusMultipleChoices || status > http.StatusPermanentRedirect {
		return ErrInternalServerError.WithError(fmt.Errorf("router: %d is not a redirect status", status))
	}
	b.res.Header().Set(HeaderLocation, location)
	b.res.WriteHeader(status)
	return nil
}

// Attachment writes the body with a Content-Disposition header, so that the
// browser saves it under the given file name. A name outside ASCII reaches the
// client through the RFC 5987 form of that header, next to an ASCII fallback.
//
// It writes the whole body. [Base.AttachmentFile] streams a file from disk
// instead, which is what a large export wants.
func (b *Base) Attachment(status int, contentType, filename string, data []byte) error {
	b.res.Header().Set(HeaderContentDisposition, contentDisposition(dispositionAttachment, filename))
	return b.Blob(status, contentType, data)
}

// Component renders itself into a writer. A template that the a-h/templ
// generator produces satisfies it, and so does any other type of that shape.
//
// The router declares the interface instead of importing templ, so the module
// keeps its promise of no dependencies. A [Component] value and a
// templ.Component value are the same thing to the compiler.
type Component interface {
	Render(ctx context.Context, w io.Writer) error
}

// ComponentFunc adapts a plain function to [Component]. Use it to reach a
// templ helper that the interface alone does not cover, such as rendering
// named fragments for an htmx request:
//
//	return c.Render(http.StatusOK, router.ComponentFunc(
//		func(ctx context.Context, w io.Writer) error {
//			return templ.RenderFragments(ctx, w, view.Page(data), "row")
//		}))
type ComponentFunc func(ctx context.Context, w io.Writer) error

// Render implements [Component].
func (f ComponentFunc) Render(ctx context.Context, w io.Writer) error { return f(ctx, w) }

// renderBufSize is the starting capacity of a render buffer.
const renderBufSize = 8 << 10

// maxPooledRenderBuf is the largest buffer that the pool keeps. The pool drops
// a bigger one, so a single large page does not pin that memory for the life
// of the process.
const maxPooledRenderBuf = 512 << 10

// renderBufs holds the buffers that [Base.Render] writes into.
var renderBufs = sync.Pool{
	New: func() any { return bytes.NewBuffer(make([]byte, 0, renderBufSize)) },
}

// keepBuf reports whether the pool keeps buf after a render. It drops one that
// a large page grew past the ceiling.
func keepBuf(buf *bytes.Buffer) bool { return buf.Cap() <= maxPooledRenderBuf }

// renderError turns a component failure into an error for the error handler.
//
// An [HTTPError] from the component passes through untouched, so a template
// that reports [ErrNotFound] still answers 404. Any other error is internal
// and becomes a 500 whose message never reaches the client.
func renderError(err error) error {
	if _, ok := errors.AsType[*HTTPError](err); ok {
		return err
	}
	return ErrInternalServerError.WithError(fmt.Errorf("router: render component: %w", err))
}

// Render writes an HTML body from a [Component].
//
// It renders into a buffer before it writes the header, so a component that
// fails halfway produces a clean 500 instead of a truncated page, and the
// response carries a Content-Length. Use [Base.RenderStream] for a page that
// is too large to hold in memory, or for one that flushes early.
//
// The component receives the [Base] as its context, so it reads the values of
// [Base.Set] through ctx.Value, and the request itself through [FromContext].
// That context is only valid until Render returns, because [NewPooled] reuses
// it.
//
// An [HTTPError] that the component returns keeps its status, so a template
// that reports [ErrNotFound] answers 404. Any other error becomes a 500.
//
// A HEAD request still runs the component, because the length of the page is
// the answer. [Base.RenderStream] skips it.
func (b *Base) Render(status int, c Component) error {
	buf := renderBufs.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if keepBuf(buf) {
			buf.Reset()
			renderBufs.Put(buf)
		}
	}()

	if err := c.Render(b, buf); err != nil {
		return renderError(err)
	}
	return b.Blob(status, MIMETextHTMLCharsetUTF8, buf.Bytes())
}

// RenderStream writes an HTML body from a [Component] straight to the client.
// It commits the response before the component runs, so templ.Flush reaches
// the [Response] and sends an early part of the page.
//
// A component that fails leaves a partial body on the wire. The error still
// reaches the error handler, which logs it and writes nothing more, because
// the response is already committed. A write that failed because the client
// went away is logged at debug level, not as a server fault.
//
// A HEAD request answers with the headers alone and never runs the component,
// so the response carries no Content-Length. [Base.Render] answers one with a
// length instead.
func (b *Base) RenderStream(status int, c Component) error {
	b.contentType(MIMETextHTMLCharsetUTF8)
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead {
		return nil
	}
	if err := c.Render(b, b.res); err != nil {
		return renderError(err)
	}
	return nil
}
