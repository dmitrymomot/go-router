package router

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	data, err := json.Marshal(v, opts...)
	if err != nil {
		return ErrInternalServerError.WithError(fmt.Errorf("router: encode JSON response: %w", err))
	}
	return b.Blob(status, MIMEApplicationJSONCharsetUTF8, data)
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
// browser saves it under the given file name.
func (b *Base) Attachment(status int, contentType, filename string, data []byte) error {
	b.res.Header().Set(HeaderContentDisposition,
		fmt.Sprintf("attachment; filename=%q", filename))
	return b.Blob(status, contentType, data)
}
