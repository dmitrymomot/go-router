package router

import (
	"context"
	"encoding"
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

const DefaultMaxBodyBytes int64 = 4 << 20

const defaultMaxMultipartMemory int64 = 32 << 20

func (b *Base) Bind[T any]() (T, error) {
	var v T

	switch b.req.Method {
	case http.MethodGet, http.MethodHead, http.MethodDelete, MethodQuery:
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
		// The bodiless methods returned above, so this one carries a body and
		// does not say what it is.
		return v, ErrUnsupportedMediaType.WithMessage("a %s body needs a Content-Type", b.req.Method)
	default:
		return v, ErrUnsupportedMediaType.WithMessage("cannot decode a %s body", ct)
	}
}

func (b *Base) BindJSON[T any](opts ...json.Options) (T, error) {
	var v T
	body := countingBody{r: b.limitedBody()}
	if err := json.UnmarshalRead(&body, &v, b.jsonOptions(opts)...); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return v, ErrPayloadTooLarge.WithError(err)
		}
		// json/v2 reports an empty body with the same syntax error as a
		// truncated one, so the byte count tells them apart.
		if body.read == 0 {
			return v, ErrBadRequest.WithMessage("the request body is empty").WithError(err)
		}
		return v, ErrBadRequest.WithMessage("malformed JSON body: %s", err).WithError(err)
	}
	if b.opts().strictBind {
		stripUntagged(reflect.ValueOf(&v).Elem())
	}
	return v, validate(&v)
}

func (b *Base) BindForm[T any]() (T, error) {
	var v T
	if err := b.parseForm(); err != nil {
		return v, err
	}
	if err := b.decodeInto(b.req.PostForm, &v, "form"); err != nil {
		return v, err
	}
	return v, validate(&v)
}

func (b *Base) BindQuery[T any]() (T, error) {
	var v T
	if err := b.decodeInto(b.queryValues(), &v, "query"); err != nil {
		return v, err
	}
	return v, validate(&v)
}

func (b *Base) BindPath[T any]() (T, error) {
	var v T
	vals := make(url.Values, len(b.paramNames))
	for i, n := range b.paramNames {
		if i < len(b.paramVals) {
			vals[n] = b.paramVals[i : i+1 : i+1]
		}
	}
	if err := b.decodeInto(vals, &v, "param"); err != nil {
		return v, err
	}
	return v, validate(&v)
}

func (b *Base) BindHeader[T any]() (T, error) {
	var v T
	if err := b.decodeInto(url.Values(b.req.Header), &v, "header"); err != nil {
		return v, err
	}
	return v, validate(&v)
}

type Validator interface {
	Validate() error
}

// validate takes &v so a Validator with a pointer receiver is found. For a
// pointer T that makes **T, whose method set is empty, so the value is tried
// too.
func validate[T any](v *T) error {
	// A body of "null" bound into a pointer leaves nothing to hand the handler,
	// and the client chose that, not the caller. Refusing it here keeps a nil
	// out of every handler that binds a pointer.
	if rv := reflect.ValueOf(*v); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return ErrBadRequest.WithMessage("the request body is null")
	}
	sv, ok := any(v).(Validator)
	if !ok {
		sv, ok = any(*v).(Validator)
	}
	if !ok {
		return nil
	}
	err := sv.Validate()
	if err == nil {
		return nil
	}
	if fields := fieldErrors(err); len(fields) > 0 {
		return ErrUnprocessableEntity.WithDetails(fields).WithError(err)
	}
	return ErrUnprocessableEntity.WithError(err)
}

func fieldErrors(err error) []FieldError {
	switch e := err.(type) {
	case FieldError:
		return []FieldError{e}
	case *FieldError:
		return []FieldError{*e}
	case *HTTPError:
		if fields, ok := e.Details.([]FieldError); ok {
			return fields
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var out []FieldError
		for _, sub := range joined.Unwrap() {
			out = append(out, fieldErrors(sub)...)
		}
		return out
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return fieldErrors(wrapped.Unwrap())
	}
	return nil
}

var selfDecoders = [...]reflect.Type{
	reflect.TypeFor[json.UnmarshalerFrom](),
	reflect.TypeFor[json.Unmarshaler](),
	reflect.TypeFor[encoding.TextUnmarshaler](),
}

func stripUntagged(rv reflect.Value) {
	rt := rv.Type()
	for _, it := range selfDecoders {
		if rt.Implements(it) || reflect.PointerTo(rt).Implements(it) {
			return
		}
	}

	switch rv.Kind() {
	case reflect.Pointer:
		if !rv.IsNil() {
			stripUntagged(rv.Elem())
		}
	case reflect.Slice, reflect.Array:
		if !holdsFields(rt.Elem()) {
			return
		}
		for i := range rv.Len() {
			stripUntagged(rv.Index(i))
		}
	case reflect.Map:
		if !holdsFields(rt.Elem()) {
			return
		}
		for _, k := range rv.MapKeys() {
			ev := reflect.New(rt.Elem()).Elem()
			ev.Set(rv.MapIndex(k))
			stripUntagged(ev)
			rv.SetMapIndex(k, ev)
		}
	case reflect.Struct:
		for _, f := range structFields(rt, "json") {
			fv := rv.Field(f.index)
			if f.embedded {
				stripUntagged(fv)
				continue
			}
			if !fv.CanSet() {
				continue
			}
			if f.tagged {
				stripUntagged(fv)
				continue
			}
			fv.SetZero()
		}
	}
}

const typeWalkLimit = 8

func holdsFields(t reflect.Type) bool {
	for range typeWalkLimit {
		switch t.Kind() {
		case reflect.Struct:
			return true
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
		default:
			return false
		}
	}
	return true
}

func (b *Base) decodeInto(vals url.Values, dst any, tag string) error {
	fields, err := decodeValues(vals, dst, tag, b.opts().strictBind)
	if err != nil {
		return ErrBadRequest.WithError(err)
	}
	if len(fields) == 0 {
		return nil
	}
	errs := make([]error, len(fields))
	for i, f := range fields {
		errs[i] = f
	}
	return ErrBadRequest.WithMessage("invalid request").
		WithDetails(fields).
		WithError(errors.Join(errs...))
}

type countingBody struct {
	r    io.Reader
	read int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

func (b *Base) limitedBody() io.ReadCloser {
	if b.req.Body == nil {
		return http.NoBody
	}
	limit := b.opts().maxBody
	if limit <= 0 {
		return b.req.Body
	}
	// MaxBytesReader marks the connection for closing through an unexported
	// method on the writer it is handed, and does not unwrap, so it needs the
	// one net/http gave us rather than the wrapper.
	return http.MaxBytesReader(b.res.ResponseWriter, b.req.Body, limit)
}

func (b *Base) parseForm() error {
	if err := b.formError(); err != nil {
		return err
	}
	// Only the type matters here, and net/http parses the whole header again
	// for the body, rejecting a malformed one there.
	ct, _, _ := strings.Cut(b.req.Header.Get(HeaderContentType), ";")
	multipart := strings.EqualFold(strings.TrimSpace(ct), MIMEMultipartForm)
	// ParseForm leaves MultipartForm nil but fills PostForm, so which field
	// says "already read" depends on the content type.
	if multipart && b.req.MultipartForm != nil || !multipart && b.req.PostForm != nil {
		return nil
	}
	b.req.Body = b.limitedBody()

	var err error
	if multipart {
		memory := b.opts().maxMultipart
		if memory <= 0 {
			memory = defaultMaxMultipartMemory
		}
		if err = b.req.ParseMultipartForm(memory); err == nil {
			removeSpilledParts(b.req)
		}
	} else {
		err = b.req.ParseForm()
	}
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return b.setFormError(ErrPayloadTooLarge.WithError(err))
	}
	return b.setFormError(ErrBadRequest.WithMessage("malformed form body").WithError(err))
}

// net/http removes these from the request it holds, but the binder parses into
// a middleware copy, so nothing else would.
func removeSpilledParts(req *http.Request) {
	form := req.MultipartForm
	if form == nil || len(form.File) == 0 {
		return
	}
	context.AfterFunc(req.Context(), func() {
		//nolint:errcheck // A file that is gone already is the wanted outcome.
		form.RemoveAll()
	})
}

func (b *Base) ParamAs[T any](name string) (T, error) {
	raw, ok := b.ParamOK(name)
	if !ok {
		var v T
		return v, ErrBadRequest.WithMessage("the route has no parameter %q", name)
	}
	return parseAs[T](raw, "route parameter", name)
}

func (b *Base) ParamAsDefault[T any](name string, def T) T {
	return ParseValueDefault(b.Param(name), def)
}

func (b *Base) QueryAs[T any](name string) (T, error) {
	return parseAs[T](b.Query(name), "query parameter", name)
}

func (b *Base) QueryAsDefault[T any](name string, def T) T {
	return ParseValueDefault(b.Query(name), def)
}

func (b *Base) QueryAsOK[T any](name string) (T, bool, error) {
	raw, ok := b.QueryOK(name)
	if !ok {
		var v T
		return v, false, nil
	}
	v, err := parseAs[T](raw, "query parameter", name)
	return v, true, err
}

func (b *Base) QueryAllAs[T any](name string) ([]T, error) {
	var out []T
	raw, ok := b.queryValues()[name]
	if !ok {
		return nil, nil
	}
	if err := setField(reflect.ValueOf(&out).Elem(), raw, ""); err != nil {
		return nil, ErrBadRequest.WithMessage("query parameter %s: %s", name, err).WithError(err)
	}
	return out, nil
}

func ParseValue[T any](s string) (T, error) {
	var v T
	if err := setScalar(reflect.ValueOf(&v).Elem(), s, ""); err != nil {
		return v, err
	}
	return v, nil
}

func parseAs[T any](raw, kind, name string) (T, error) {
	v, err := ParseValue[T](raw)
	if err != nil {
		return v, ErrBadRequest.WithMessage("%s %s: %s", kind, name, err).WithError(err)
	}
	return v, nil
}

func ParseValueDefault[T any](s string, def T) T {
	if s == "" {
		return def
	}
	v, err := ParseValue[T](s)
	if err != nil {
		return def
	}
	return v
}

func (b *Base) FormValue(name string) string {
	//nolint:errcheck // The caller asked for a value, not for the parse error.
	b.parseForm()
	return b.req.PostForm.Get(name)
}

func (b *Base) FormDefault(name, def string) string {
	//nolint:errcheck // Same as FormValue.
	b.parseForm()
	if v := b.req.PostForm[name]; len(v) > 0 && v[0] != "" {
		return v[0]
	}
	return def
}

func (b *Base) FormValues() (url.Values, error) {
	if err := b.parseForm(); err != nil {
		return nil, err
	}
	return b.req.PostForm, nil
}

func (b *Base) FormAs[T any](name string) (T, error) {
	var v T
	if err := b.parseForm(); err != nil {
		return v, err
	}
	return parseAs[T](b.req.PostForm.Get(name), "form field", name)
}

func (b *Base) FormFile(name string) (multipart.File, *multipart.FileHeader, error) {
	fhs, err := b.formFiles(name)
	if err != nil {
		return nil, nil, err
	}
	f, err := fhs[0].Open()
	if err != nil {
		return nil, nil, ErrBadRequest.WithMessage("cannot read the file named %q", name).WithError(err)
	}
	return f, fhs[0], nil
}

func (b *Base) FormFiles(name string) ([]*multipart.FileHeader, error) {
	return b.formFiles(name)
}

func (b *Base) formFiles(name string) ([]*multipart.FileHeader, error) {
	if err := b.parseForm(); err != nil {
		return nil, err
	}
	if b.req.MultipartForm != nil {
		if fhs := b.req.MultipartForm.File[name]; len(fhs) > 0 {
			return fhs, nil
		}
	}
	return nil, ErrBadRequest.WithMessage("no uploaded file named %q", name).WithError(http.ErrMissingFile)
}

func (b *Base) MultipartForm() (*multipart.Form, error) {
	if err := b.parseForm(); err != nil {
		return nil, err
	}
	if b.req.MultipartForm == nil {
		return nil, ErrBadRequest.WithMessage("the request body is not a multipart form")
	}
	return b.req.MultipartForm, nil
}
