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

func (b *Base) Stringf(status int, format string, args ...any) error {
	return b.String(status, fmt.Sprintf(format, args...))
}

func (b *Base) HTML(status int, html string) error {
	b.contentType(MIMETextHTMLCharsetUTF8)
	return b.String(status, html)
}

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

func (b *Base) JSONPretty(status int, v any, indent string) error {
	return b.JSON(status, v, jsontext.Multiline(true), jsontext.WithIndent(indent))
}

func (b *Base) Stream(status int, contentType string, r io.Reader) error {
	b.contentType(contentType)
	b.res.WriteHeader(status)
	if b.req.Method == http.MethodHead {
		return nil
	}
	_, err := io.Copy(b.res, r)
	return err
}

func (b *Base) NoContent(status int) error {
	b.res.WriteHeader(status)
	return nil
}

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

func (b *Base) Attachment(status int, contentType, filename string, data []byte) error {
	b.res.Header().Set(HeaderContentDisposition, contentDisposition(dispositionAttachment, filename))
	return b.Blob(status, contentType, data)
}

type Component interface {
	Render(ctx context.Context, w io.Writer) error
}

type ComponentFunc func(ctx context.Context, w io.Writer) error

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
