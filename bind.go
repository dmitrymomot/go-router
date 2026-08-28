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

// DefaultMaxBodyBytes is the request body limit that a new router applies to
// [Base.Bind] and [Base.BindJSON]. Change it per router with
// [Router.MaxBodyBytes].
const DefaultMaxBodyBytes int64 = 4 << 20 // 4 MiB

// defaultMaxMultipartMemory is the memory that a multipart body uses before a
// part spills to a temporary file, for a router that names no limit of its
// own. It is the one that [http.Request.ParseMultipartForm] applies.
const defaultMaxMultipartMemory int64 = 32 << 20 // 32 MiB

// Bind decodes the request into a value of type T. It reads the media type of
// the request and dispatches to JSON, to a form, or to the query string:
//
//	in, err := c.Bind[CreateUser]()
//
// A GET, HEAD or DELETE request has no body, so Bind reads the query string,
// and so does a [MethodQuery] request, whose body carries a query in a media
// type that the sender chooses. A body of an unsupported media type produces a
// 415.
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
		return b.BindQuery[T]()
	default:
		return v, ErrUnsupportedMediaType.WithMessage("cannot decode a %s body", ct)
	}
}

// BindJSON decodes the JSON request body into a value of type T. It uses
// encoding/json/v2, which rejects invalid UTF-8 and duplicate object names.
//
// A field that no json tag names reads the key that its own name spells, so a
// body reaches a field that the type never meant to expose: a request that
// spells IsAdmin fills an IsAdmin field. Tag every field the client sends, or
// set [Router.StrictBind], which blanks the fields that no tag names, in the
// value and in every struct it holds.
func (b *Base) BindJSON[T any](opts ...json.Options) (T, error) {
	var v T
	body := countingBody{r: b.limitedBody()}
	if err := json.UnmarshalRead(&body, &v, b.jsonOptions(opts)...); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return v, ErrPayloadTooLarge.WithError(err)
		}
		// encoding/json/v2 reports an empty body as a syntax error of its own,
		// the same one that a truncated body reports, so the bytes that the
		// decoder read are what tells the two apart.
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

// BindForm decodes an HTML form body into a value of type T. It reads the
// field names from the form tag, then from the json tag, then from the field
// name itself.
//
// A field that no tag names therefore reads the key that its own name spells,
// as written and in lower case, so a request fills a field that the form never
// showed: a form that posts name and email still reaches an IsAdmin field with
// isadmin=true. Tag every field the form posts, or set [Router.StrictBind],
// which fills only the fields that a tag names.
//
// The body obeys [Router.MaxBodyBytes], and a part of a multipart body spills
// to a temporary file past [Router.MaxMultipartMemory].
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

// BindQuery decodes the query string into a value of type T. It reads the
// field names from the query tag, then from the json tag, then from the field
// name itself.
func (b *Base) BindQuery[T any]() (T, error) {
	var v T
	if err := b.decodeInto(b.queryValues(), &v, "query"); err != nil {
		return v, err
	}
	return v, validate(&v)
}

// BindPath decodes the parameters of the matched route into a value of type T.
// It reads the field names from the param tag, then from the json tag, then
// from the field name itself:
//
//	type ref struct {
//		Org  string `param:"org"`
//		Repo string `param:"repo"`
//	}
//
// A parameter that the route does not declare leaves its field alone.
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

// BindHeader decodes the request headers into a value of type T. It reads the
// field names from the header tag, then from the json tag, then from the field
// name itself:
//
//	type meta struct {
//		RequestID string `header:"x-request-id"`
//	}
//
// net/http stores a header under its canonical name, so a tag matches in any
// case: x-request-id finds X-Request-Id.
func (b *Base) BindHeader[T any]() (T, error) {
	var v T
	if err := b.decodeInto(url.Values(b.req.Header), &v, "header"); err != nil {
		return v, err
	}
	return v, validate(&v)
}

// Validator is a bound value that checks itself. Every Bind method calls
// Validate after it fills the value, so the check lives on the type it guards
// and nothing has to be registered:
//
//	func (in *CreateUser) Validate() error {
//		if !strings.Contains(in.Email, "@") {
//			return router.FieldError{Field: "email", Message: "is not an address"}
//		}
//		return nil
//	}
//
// An error produces a 422. Return a [FieldError], or several of them joined
// with [errors.Join], and the client reads the failures field by field in
// [HTTPError.Details].
type Validator interface {
	// Validate reports whether the value is fit to use.
	Validate() error
}

// validate runs the self check of a bound value, for a type that has one.
func validate(v any) error {
	sv, ok := v.(Validator)
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

// fieldErrors collects every [FieldError] that err carries, in the order in
// which it holds them. It reaches through [errors.Join] and through the
// details of an [HTTPError], which are the two shapes that a Validate method
// returns when it names the fields that failed.
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

// selfDecoders are the interfaces through which a type reads its own
// representation. Such a type decides for itself which of its fields a request
// reaches, which is why [stripUntagged] leaves it alone.
var selfDecoders = [...]reflect.Type{
	reflect.TypeFor[json.UnmarshalerFrom](),
	reflect.TypeFor[json.Unmarshaler](),
	reflect.TypeFor[encoding.TextUnmarshaler](),
}

// stripUntagged blanks every field that no json tag names, in rv and in the
// values that rv holds. [Router.StrictBind] promises that a request fills only
// the fields that a tag names, and the JSON decoder matches an untagged
// exported field under the name that the field itself spells, so the promise
// holds by clearing those fields once the decode is through.
//
// A type that reads its own JSON or its own text keeps what it read, as an
// [encoding.TextUnmarshaler] does on the form path.
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
		// A map value is not addressable, so it comes out, loses the fields
		// that no tag names, and goes back in.
		for _, k := range rv.MapKeys() {
			ev := reflect.New(rt.Elem()).Elem()
			ev.Set(rv.MapIndex(k))
			stripUntagged(ev)
			rv.SetMapIndex(k, ev)
		}
	case reflect.Struct:
		for _, f := range structFields(rt, "json") {
			fv := rv.Field(f.index)
			// An embedded struct promotes its fields, and each of them obeys
			// the rule on its own. reflect sets those fields even when the
			// type that carries them is unexported, which is why the check
			// below belongs to the named fields alone.
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

// typeWalkLimit bounds the walk of [holdsFields]. A type that nests deeper
// than this is one that no request describes, and the walk answers yes for it,
// so that a strange type loses the fields that no tag names rather than
// keeping them.
const typeWalkLimit = 8

// holdsFields reports whether a value of t can carry struct fields. It keeps
// [stripUntagged] from walking a slice of bytes or a map of strings element by
// element.
func holdsFields(t reflect.Type) bool {
	for range typeWalkLimit {
		switch t.Kind() {
		case reflect.Struct:
			return true
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
		default:
			// An interface reads as a map and never as a struct of the
			// application, so it holds no field that a tag could name.
			return false
		}
	}
	return true
}

// decodeInto runs the form decoder and turns its errors into a 400. It reports
// every field that did not fit, in [HTTPError.Details], so that a form
// re-renders with a message on each of them and not only on the first.
func (b *Base) decodeInto(vals url.Values, dst any, tag string) error {
	fields, err := decodeValues(vals, dst, tag, b.opts().strictBind)
	if err != nil {
		// A target that is not a pointer to a struct is a fault of the handler
		// and not of the request, so the text of the decoder goes to the log
		// and the client reads the standard message of the status. The status
		// stays a 400: [Base.Bind] reads the query string for a body that
		// names no media type, which is the path a client reaches this on.
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

// countingBody counts the bytes that it handed out. [Base.BindJSON] reads
// the count to tell an empty body from a malformed one, which the error of the
// decoder does not say.
type countingBody struct {
	r    io.Reader
	read int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

// limitedBody caps the request body at the limit of the router.
func (b *Base) limitedBody() io.ReadCloser {
	if b.req.Body == nil {
		return http.NoBody
	}
	limit := b.opts().maxBody
	if limit <= 0 {
		return b.req.Body
	}
	return http.MaxBytesReader(b.res, b.req.Body, limit)
}

// parseForm parses the body of the request as a form. Every reader of a form
// goes through it, so a form body obeys [Router.MaxBodyBytes] just as a JSON
// body does, and a multipart part spills to disk past
// [Router.MaxMultipartMemory].
//
// It is idempotent: net/http parses a body once and answers from the form it
// parsed then, so the cap goes on the body at most once, and a body that it
// could not read reports the same failure to every reader after the first.
func (b *Base) parseForm() error {
	if err := b.formError(); err != nil {
		return err
	}
	if b.req.PostForm != nil {
		// The body is read already, by an earlier call or by the handler
		// itself, and net/http answers from what it parsed then. Wrapping the
		// body again would cap a reader that nothing reads.
		return nil
	}
	b.req.Body = b.limitedBody()

	var err error
	if ct, _, _ := mime.ParseMediaType(b.req.Header.Get(HeaderContentType)); ct == MIMEMultipartForm {
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

// removeSpilledParts hangs the temporary files of a multipart body on the
// context of the request, which ends when the request does.
//
// net/http removes them itself, but from the request that the server holds,
// and middleware hands the context a copy of that request. The binder parses
// into the copy, so the server finds no form on the request it kept and
// removes nothing, and the files sit on disk for the life of the process. The
// context outlives the copy, so it is what the files hang on.
//
// It runs for a body that parsed. One that did not leaves no file behind,
// because [multipart.Reader.ReadForm] removes its own on the way out.
func removeSpilledParts(req *http.Request) {
	form := req.MultipartForm
	if form == nil || len(form.File) == 0 {
		return
	}
	// The cleanup holds the form and never the [Base], which goes back to the
	// pool and belongs to another request by the time this runs.
	context.AfterFunc(req.Context(), func() {
		//nolint:errcheck // A file that is gone already is the wanted outcome.
		form.RemoveAll()
	})
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
	raw, ok := b.ParamOK(name)
	if !ok {
		var v T
		return v, ErrBadRequest.WithMessage("the route has no parameter %q", name)
	}
	return parseAs[T](raw, "route parameter", name)
}

// ParamAsDefault returns a route parameter parsed into T, or def when the
// route does not declare it, when it is empty, and when it does not parse.
func (b *Base) ParamAsDefault[T any](name string, def T) T {
	return ParseValueDefault(b.Param(name), def)
}

// QueryAs returns a query parameter parsed into T. A missing or empty
// parameter yields the zero value of T and no error; use [Base.QueryValues] to
// tell an absent parameter from an empty one.
func (b *Base) QueryAs[T any](name string) (T, error) {
	return parseAs[T](b.Query(name), "query parameter", name)
}

// QueryAsDefault returns a query parameter parsed into T, or def when the
// parameter is absent, empty, or does not parse.
func (b *Base) QueryAsDefault[T any](name string, def T) T {
	return ParseValueDefault(b.Query(name), def)
}

// QueryAsOK returns a query parameter parsed into T and reports whether the
// query holds it. An empty value is a value: "?page=" answers the zero value
// of T, true and no error.
func (b *Base) QueryAsOK[T any](name string) (T, bool, error) {
	raw, ok := b.QueryOK(name)
	if !ok {
		var v T
		return v, false, nil
	}
	v, err := parseAs[T](raw, "query parameter", name)
	return v, true, err
}

// QueryAllAs returns every value of a repeated query parameter, parsed into T:
//
//	tags, err := c.QueryAllAs[string]("tag") // ?tag=go&tag=http
//
// A parameter that the query does not hold yields a nil slice and no error.
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

// ParseValue parses a string into a value of type T, with the table that the
// binder itself uses: the string, boolean, integer and float kinds,
// [time.Duration], any type that implements [encoding.TextUnmarshaler] such as
// [time.Time], and a pointer to one of those.
//
// An empty string yields the zero value of T and no error, as it does in a
// bound struct. The error is a plain one, not an [HTTPError], because the
// caller knows what the value stands for and what status it deserves.
func ParseValue[T any](s string) (T, error) {
	var v T
	if err := setScalar(reflect.ValueOf(&v).Elem(), s, ""); err != nil {
		return v, err
	}
	return v, nil
}

// parseAs parses raw into T and names the value that failed, so the client
// reads which parameter it got wrong. kind names the place the value came
// from, such as "query parameter".
func parseAs[T any](raw, kind, name string) (T, error) {
	v, err := ParseValue[T](raw)
	if err != nil {
		return v, ErrBadRequest.WithMessage("%s %s: %s", kind, name, err).WithError(err)
	}
	return v, nil
}

// ParseValueDefault parses a string into a value of type T, and returns def
// when the string is empty or does not parse.
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

// FormValue returns the first value that the body posts under a name, or an
// empty string. It reads the body alone and never the query string, so a
// parameter in the URL cannot forge a field of the form.
//
// It swallows the error of a body that does not parse, as [Base.Query] has
// none to report. Use [Base.FormValues] to see it.
func (b *Base) FormValue(name string) string {
	//nolint:errcheck // The caller asked for a value, not for the parse error.
	b.parseForm()
	return b.req.PostForm.Get(name)
}

// FormDefault returns the first value that the body posts under a name, or def
// when the body does not hold it and when it holds it empty. It reads the body
// alone and never the query string, and it swallows the error of a body that
// does not parse.
func (b *Base) FormDefault(name, def string) string {
	//nolint:errcheck // Same as FormValue.
	b.parseForm()
	if v := b.req.PostForm[name]; len(v) > 0 && v[0] != "" {
		return v[0]
	}
	return def
}

// FormValues returns every value that the body posts. It reads the body alone
// and never the query string, so a parameter in the URL cannot forge a field
// of the form.
//
// The request holds the parsed body, and every caller reads the same map, so
// treat it as read only. Copy it with [maps.Clone] before a change.
func (b *Base) FormValues() (url.Values, error) {
	if err := b.parseForm(); err != nil {
		return nil, err
	}
	return b.req.PostForm, nil
}

// FormAs returns a field of the body parsed into T. It reads the body alone
// and never the query string. A missing or empty field yields the zero value
// of T and no error; a value that does not parse produces a 400.
func (b *Base) FormAs[T any](name string) (T, error) {
	var v T
	if err := b.parseForm(); err != nil {
		return v, err
	}
	return parseAs[T](b.req.PostForm.Get(name), "form field", name)
}

// FormFile returns an uploaded file from a multipart body. The caller closes
// the file.
//
// It parses the body under the limits of the router, so an upload past
// [Router.MaxBodyBytes] produces a 413 even in a handler that binds nothing
// else. A field that holds no file produces a 400.
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

// FormFiles returns every file that the body posts under one name, which is
// what a picker that takes more than one file sends. The caller opens and
// closes each of them:
//
//	for _, fh := range files {
//		f, err := fh.Open()
//	}
//
// A field that holds no file produces a 400.
func (b *Base) FormFiles(name string) ([]*multipart.FileHeader, error) {
	return b.formFiles(name)
}

// formFiles is the lookup behind [Base.FormFile] and [Base.FormFiles].
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

// MultipartForm returns the parsed multipart body, its values and its files.
// It parses the body under the limits of the router, and reports a 400 for a
// body that is not multipart.
//
// [http.Request.MultipartReader] streams the parts instead, and the two are
// mutually exclusive. Everything here parses the whole body first, so a
// handler that reads one field this way can no longer stream the rest, and a
// handler that took the reader gets a 400 out of every method here that
// reports an error.
func (b *Base) MultipartForm() (*multipart.Form, error) {
	if err := b.parseForm(); err != nil {
		return nil, err
	}
	if b.req.MultipartForm == nil {
		return nil, ErrBadRequest.WithMessage("the request body is not a multipart form")
	}
	return b.req.MultipartForm, nil
}
