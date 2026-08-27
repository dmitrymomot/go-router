package router

import (
	"encoding/json/v2"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

// DefaultMaxBodyBytes is the request body limit that a new router applies to
// [Base.Bind] and [Base.BindJSON]. Change it per router with
// [Router.MaxBodyBytes].
const DefaultMaxBodyBytes int64 = 4 << 20 // 4 MiB

// Bind decodes the request into a value of type T. It reads the media type of
// the request and dispatches to JSON, to a form, or to the query string:
//
//	in, err := c.Bind[CreateUser]()
//
// A GET, HEAD or DELETE request has no body, so Bind reads the query string.
// A body of an unsupported media type produces a 415.
func (b *Base) Bind[T any]() (T, error) {
	var v T

	switch b.req.Method {
	case http.MethodGet, http.MethodHead, http.MethodDelete:
		return b.BindQuery[T]()
	}

	ct, _, err := mime.ParseMediaType(b.req.Header.Get(HeaderContentType))
	if err != nil && b.req.Header.Get(HeaderContentType) != "" {
		return v, ErrUnsupportedMediaType.WithMessage("malformed Content-Type").WithError(err)
	}

	switch {
	case ct == MIMEApplicationJSON || strings.HasSuffix(ct, "+json"):
		return b.BindJSON[T]()
	case ct == MIMEApplicationForm, ct == MIMEMultipartForm:
		return b.BindForm[T]()
	case ct == "":
		return b.BindQuery[T]()
	default:
		return v, ErrUnsupportedMediaType.WithMessage("cannot decode a %s body", ct)
	}
}

// BindJSON decodes the JSON request body into a value of type T. It uses
// encoding/json/v2, which rejects invalid UTF-8 and duplicate object names.
func (b *Base) BindJSON[T any](opts ...json.Options) (T, error) {
	var v T
	body := b.limitedBody()
	if err := json.UnmarshalRead(body, &v, b.jsonOptions(opts)...); err != nil {
		if errors.Is(err, io.EOF) {
			return v, ErrBadRequest.WithMessage("the request body is empty").WithError(err)
		}
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return v, ErrPayloadTooLarge.WithError(err)
		}
		return v, ErrBadRequest.WithMessage("malformed JSON body: %s", err).WithError(err)
	}
	return v, nil
}

// BindForm decodes an HTML form body into a value of type T. It reads the
// field names from the form tag, then from the json tag, then from the field
// name itself.
func (b *Base) BindForm[T any]() (T, error) {
	var v T
	ct, _, _ := mime.ParseMediaType(b.req.Header.Get(HeaderContentType))
	b.req.Body = b.limitedBody()

	var err error
	if ct == MIMEMultipartForm {
		err = b.req.ParseMultipartForm(32 << 20)
	} else {
		err = b.req.ParseForm()
	}
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return v, ErrPayloadTooLarge.WithError(err)
		}
		return v, ErrBadRequest.WithMessage("malformed form body").WithError(err)
	}
	return v, decodeInto(url.Values(b.req.PostForm), &v, "form")
}

// BindQuery decodes the query string into a value of type T. It reads the
// field names from the query tag, then from the json tag, then from the field
// name itself.
func (b *Base) BindQuery[T any]() (T, error) {
	var v T
	return v, decodeInto(b.req.URL.Query(), &v, "query")
}

// decodeInto runs the form decoder and turns its errors into a 400.
func decodeInto(vals url.Values, dst any, tag string) error {
	if err := decodeValues(vals, dst, tag); err != nil {
		return ErrBadRequest.WithMessage("%s", err).WithError(err)
	}
	return nil
}

// limitedBody caps the request body at the limit of the router.
func (b *Base) limitedBody() io.ReadCloser {
	if b.req.Body == nil {
		return http.NoBody
	}
	if b.maxBody <= 0 {
		return b.req.Body
	}
	return http.MaxBytesReader(b.res, b.req.Body, b.maxBody)
}

// ParamAs returns a route parameter parsed into T. T may be any type that
// [Base.BindQuery] accepts, such as int, bool, time.Duration or a type that
// implements [encoding.TextUnmarshaler]:
//
//	id, err := c.ParamAs[uuid.UUID]("id")
//
// A parameter that the route does not declare, or a value that does not parse,
// produces a 400.
func (b *Base) ParamAs[T any](name string) (T, error) {
	var v T
	raw, ok := b.ParamOK(name)
	if !ok {
		return v, ErrBadRequest.WithMessage("the route has no parameter %q", name)
	}
	if err := setScalar(reflect.ValueOf(&v).Elem(), raw); err != nil {
		return v, ErrBadRequest.WithMessage("route parameter %s: %s", name, err).WithError(err)
	}
	return v, nil
}

// QueryAs returns a query parameter parsed into T. A missing or empty
// parameter yields the zero value of T and no error; use [Base.QueryValues] to
// tell an absent parameter from an empty one.
func (b *Base) QueryAs[T any](name string) (T, error) {
	var v T
	raw := b.Query(name)
	if raw == "" {
		return v, nil
	}
	if err := setScalar(reflect.ValueOf(&v).Elem(), raw); err != nil {
		return v, ErrBadRequest.WithMessage("query parameter %s: %s", name, err).WithError(err)
	}
	return v, nil
}

// QueryAsDefault returns a query parameter parsed into T, or def when the
// parameter is absent, empty, or does not parse.
func (b *Base) QueryAsDefault[T any](name string, def T) T {
	v, err := b.QueryAs[T](name)
	if err != nil || b.Query(name) == "" {
		return def
	}
	return v
}

// FormFile returns an uploaded file from a multipart body. The caller closes
// the file.
func (b *Base) FormFile(name string) (multipart.File, *multipart.FileHeader, error) {
	f, h, err := b.req.FormFile(name)
	if err != nil {
		return nil, nil, ErrBadRequest.WithMessage("no uploaded file named %q", name).WithError(err)
	}
	return f, h, nil
}
