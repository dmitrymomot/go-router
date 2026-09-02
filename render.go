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

func (b *Base) contentType(value string) {
	// An empty value is not a content type. Setting one writes a blank header,
	// and net/http sniffs only when the key is absent, so the response went out
	// with "Content-Type: " and the client had nothing to go on.
	if value == "" {
		return
	}
	h := b.res.Header()
	if h.Get(HeaderContentType) == "" {
		h.Set(HeaderContentType, value)
	}
}

// Blob writes data with contentType and status, and sets Content-Length. An
// empty contentType leaves the header for net/http to sniff, and a
// Content-Type already set is left alone.
//
// A HEAD request gets the headers and no body.
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

// String writes s as text/plain with status.
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

// Stringf writes the text that format and args build, as [Base.String] does.
func (b *Base) Stringf(status int, format string, args ...any) error {
	return b.String(status, fmt.Sprintf(format, args...))
}

// HTML writes html as text/html with status. The string goes out as it
// stands, so anything built from user input has to be escaped first; see
// [Base.Render] for a template or a component.
func (b *Base) HTML(status int, html string) error {
	b.contentType(MIMETextHTMLCharsetUTF8)
	return b.String(status, html)
}

// JSON encodes v and writes it as application/json with status. opts win over
// the options of [Router.JSONOptions]. A value that does not encode reports an
// [ErrInternalServerError] and writes nothing, so the error handler still owns
// the response.
func (b *Base) JSON(status int, v any, opts ...json.Options) error {
	data, err := json.Marshal(v, b.jsonOptions(opts)...)
	if err != nil {
		return ErrInternalServerError.WithError(fmt.Errorf("router: encode JSON response: %w", err))
	}
	return b.Blob(status, MIMEApplicationJSONCharsetUTF8, data)
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

// JSONPretty is [Base.JSON] with a line per field and indent per level.
func (b *Base) JSONPretty(status int, v any, indent string) error {
	return b.JSON(status, v, jsontext.Multiline(true), jsontext.WithIndent(indent))
}

// Stream copies r to the response with contentType and status. It sets no
// Content-Length, so the answer is chunked.
func (b *Base) Stream(status int, contentType string, r io.Reader) error {
	b.contentType(contentType)
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead {
		return nil
	}
	_, err := io.Copy(b.res, r)
	return err
}

// NoContent writes status and no body.
func (b *Base) NoContent(status int) error {
	b.res.WriteHeader(status)
	return nil
}

// Redirect sets Location and writes status, which has to be one of 300, 301,
// 302, 303, 307 and 308. Any other status reports an
// [ErrInternalServerError] and writes nothing.
func (b *Base) Redirect(status int, location string) error {
	switch status {
	case http.StatusMultipleChoices, http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return ErrInternalServerError.WithError(fmt.Errorf("router: %d is not a redirect status", status))
	}
	b.res.Header().Set(HeaderLocation, location)
	b.res.WriteHeader(status)
	return nil
}

// Attachment writes data as a download named filename, through a
// Content-Disposition header. See [Base.File] to send a file from disk.
func (b *Base) Attachment(status int, contentType, filename string, data []byte) error {
	b.res.Header().Set(HeaderContentDisposition, contentDisposition(dispositionAttachment, filename))
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

// RenderStream renders c straight to the client, which suits a large page and
// a slow one. The header goes out first, so a component that fails halfway
// leaves a truncated body and the error handler cannot answer.
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
