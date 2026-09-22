package router

import (
	"context"
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

// DefaultMaxBodyBytes is the request body that the Bind methods read before
// they report [ErrPayloadTooLarge]. [Router.MaxBodyBytes] changes it, and
// [Base.SetBodyLimit] changes it for one request.
const DefaultMaxBodyBytes int64 = 4 << 20

const defaultMaxMultipartMemory int64 = 32 << 20

// Bind fills a T from the request and validates it. A GET, HEAD, DELETE or
// QUERY request binds from the query; anything else binds from the body, by
// its Content-Type: JSON for application/json and any "+json" type, and a form
// for the two form types.
//
// T names its sources with struct tags, one per source: `json`, `form`,
// `query`, `param` for a route parameter, and `header`. A T that implements
// [Validator] is validated last, and a failure becomes an
// [ErrUnprocessableEntity] whose Details hold the [FieldError] list.
//
// The error is an [HTTPError], so a handler can return it as it stands.
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

// BindJSON decodes the body as JSON into a T and validates it. opts win over
// the options of [Router.JSONOptions].
//
// An empty body, a malformed one and a body over the limit each report their
// own [HTTPError].
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
	return v, validate(&v)
}

// BindForm fills a T from the form of the body and validates it. It reads a
// URL-encoded form and a multipart one alike, through the `form` tag. Parsing
// happens once per request, so a later form read costs nothing. A bool field
// reads a checkbox: "on" when it is checked, and false when it is absent.
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

// BindQuery fills a T from the query string and validates it, through the
// `query` tag.
func (b *Base) BindQuery[T any]() (T, error) {
	var v T
	if err := b.decodeInto(b.queryValues(), &v, "query"); err != nil {
		return v, err
	}
	return v, validate(&v)
}

// BindPath fills a T from the route parameters and validates it, through the
// `param` tag.
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

// BindHeader fills a T from the request headers and validates it, through the
// `header` tag, whose value is the header name.
func (b *Base) BindHeader[T any]() (T, error) {
	var v T
	if err := b.decodeInto(url.Values(b.req.Header), &v, "header"); err != nil {
		return v, err
	}
	return v, validate(&v)
}

// Validator is a bound value that checks itself. Every Bind method calls
// Validate after it fills the value. Return a [FieldError], a slice of them
// joined with [errors.Join], or any other error, which becomes a plain
// [ErrUnprocessableEntity].
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
	if fields := FieldErrorsOf(err); fields != nil {
		return ErrUnprocessableEntity.WithDetails(fields).WithError(err)
	}
	return ErrUnprocessableEntity.WithError(err)
}

func (b *Base) decodeInto(vals url.Values, dst any, tag string) error {
	fields, err := decodeValues(vals, dst, tag)
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
	return badFields(fields, errors.Join(errs...))
}

// badFields is the 400 of a value that did not decode, one [FieldError] per
// field that failed.
func badFields(fields []FieldError, cause error) *HTTPError {
	return ErrBadRequest.WithMessage("invalid request").WithDetails(fields).WithError(cause)
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

// SetBodyLimit caps the body of this request at n bytes, in place of the cap
// of [Router.MaxBodyBytes], above or below it. The Bind methods and the form
// readers stop there with [ErrPayloadTooLarge], and a handler that reads
// Request().Body itself meets an [*http.MaxBytesError]. n of zero or less lifts
// the cap of the router, but not a cap already on the body, so of two calls
// the smaller cap wins. A request without a body is left alone.
//
// It applies to what is still unread: a form parsed before the call keeps the
// cap it was read under.
func (b *Base) SetBodyLimit(n int64) {
	if b.req.Body == nil || b.req.Body == http.NoBody {
		return
	}
	if n <= 0 {
		b.deferrals().bodyLimit = -1
		return
	}
	b.deferrals().bodyLimit = n
	b.req.Body = http.MaxBytesReader(innermostWriter(b.res), b.req.Body, n)
}

func (b *Base) bodyLimit() int64 {
	if b.deferred != nil && b.deferred.bodyLimit != 0 {
		return b.deferred.bodyLimit
	}
	return b.opts().maxBody
}

// limitedBody caps the body again even after SetBodyLimit did, because
// Decompress may have swapped the body since.
func (b *Base) limitedBody() io.ReadCloser {
	if b.req.Body == nil {
		return http.NoBody
	}
	limit := b.bodyLimit()
	if limit <= 0 {
		return b.req.Body
	}
	return http.MaxBytesReader(innermostWriter(b.res), b.req.Body, limit)
}

// innermostWriter follows Unwrap to the writer net/http created. MaxBytesReader
// marks the connection for closing through an unexported method on the writer
// it is handed, and does not unwrap, so a wrapper such as the one of Gzip
// would keep the connection open after a 413.
func innermostWriter(w http.ResponseWriter) http.ResponseWriter {
	for range unwrapLimit {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		w = u.Unwrap()
	}
	return w
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
	// net/http fills Form from the query as well, and fails the whole parse on
	// a malformed query. A Form already in place makes it skip the query. One
	// the router put there itself goes again afterwards, so net/http builds
	// the full Form when asked; one an earlier reader built stays.
	preset := b.req.Form == nil
	if preset {
		b.req.Form = make(url.Values)
	}

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
	if preset {
		b.req.Form = nil
	}
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return b.setFormError(ErrPayloadTooLarge.WithError(err))
	}
	return b.setFormError(ErrBadRequest.WithMessage("malformed form body").WithError(err))
}

// net/http removes these from the request it holds, but the binder may parse
// into a copy that Decompress or a SetRequest made, so nothing else would.
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

// ParamAs reads route parameter name as a T. T may be any string, bool,
// integer, float, time.Duration or time.Time, or a type that implements
// encoding.TextUnmarshaler. A bool takes what strconv.ParseBool takes, and
// also on and off.
//
// A missing parameter and one that does not parse each report an
// [ErrBadRequest].
func (b *Base) ParamAs[T any](name string) (T, error) {
	raw, ok := b.ParamOK(name)
	if !ok {
		var v T
		return v, ErrBadRequest.WithMessage("the route has no parameter %q", name)
	}
	return parseAs[T](raw, "route parameter", name)
}

// ParamAsDefault reads route parameter name as a T, or reports def when the
// parameter is absent, empty or malformed.
func (b *Base) ParamAsDefault[T any](name string, def T) T {
	return ParseValueDefault(b.Param(name), def)
}

// QueryAs reads query parameter name as a T. An absent parameter parses as the
// zero value of T, which for a number is an [ErrBadRequest]; use
// [Base.QueryAsOK] to tell the two apart.
func (b *Base) QueryAs[T any](name string) (T, error) {
	return parseAs[T](b.Query(name), "query parameter", name)
}

// QueryAsDefault reads query parameter name as a T, or reports def when the
// parameter is absent, empty or malformed.
func (b *Base) QueryAsDefault[T any](name string, def T) T {
	return ParseValueDefault(b.Query(name), def)
}

// QueryAsOK reads query parameter name as a T. ok is false when the query
// carries no such parameter, and the error is nil in that case.
func (b *Base) QueryAsOK[T any](name string) (T, bool, error) {
	raw, ok := b.QueryOK(name)
	if !ok {
		var v T
		return v, false, nil
	}
	v, err := parseAs[T](raw, "query parameter", name)
	return v, true, err
}

// QueryAllAs reads every value of query parameter name as a T, for a parameter
// the client repeats. It reports a nil slice when the parameter is absent.
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

// ParseValue parses s as a T. T may be any string, bool, integer, float,
// time.Duration or time.Time, or a type that implements
// encoding.TextUnmarshaler. A bool takes what strconv.ParseBool takes, and
// also on and off. The error is the parse failure itself, without a status,
// and a pointer T is nil on error.
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

// ParseValueDefault parses s as a T, or reports def when s is empty or does
// not parse.
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

// FormValue reads form field name from the body, or "" when the field is
// absent or the body does not parse. Unlike [http.Request.FormValue] it reads
// the body alone and never the query. Use [Base.FormValues] to see the parse
// error, and [Base.FormRequired] for a field that must be there. The ParseForm
// middleware refuses a body that does not parse before the handler runs.
func (b *Base) FormValue(name string) string {
	//nolint:errcheck // The caller asked for a value, not for the parse error.
	b.parseForm()
	return b.req.PostForm.Get(name)
}

// FormDefault reads form field name from the body, or reports def when the
// field is absent or empty, or the body does not parse.
func (b *Base) FormDefault(name, def string) string {
	//nolint:errcheck // Same as FormValue.
	b.parseForm()
	if v := b.req.PostForm[name]; len(v) > 0 && v[0] != "" {
		return v[0]
	}
	return def
}

// FormRequired reads form field name from the body. An absent or empty field
// reports an [ErrBadRequest] whose Details hold one [FieldError] for name, as
// [Base.Bind] reports one. A body that does not parse reports what
// [Base.FormValues] reports.
func (b *Base) FormRequired(name string) (string, error) {
	if err := b.parseForm(); err != nil {
		return "", err
	}
	if v := b.req.PostForm.Get(name); v != "" {
		return v, nil
	}
	fe := FieldError{Field: name, Message: "is required"}
	return "", badFields([]FieldError{fe}, fe)
}

// FormValues reports the parsed form of the body. The router parses it once
// per request and hands back the same map, so the caller must not change it.
func (b *Base) FormValues() (url.Values, error) {
	if err := b.parseForm(); err != nil {
		return nil, err
	}
	return b.req.PostForm, nil
}

// FormAs reads form field name as a T. See [Base.ParamAs] for the types it
// takes.
func (b *Base) FormAs[T any](name string) (T, error) {
	var v T
	if err := b.parseForm(); err != nil {
		return v, err
	}
	return parseAs[T](b.req.PostForm.Get(name), "form field", name)
}

// FormFile opens the first file uploaded under name. The caller closes the
// file. A request with no such file reports an [ErrBadRequest] that wraps
// [http.ErrMissingFile].
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

// FormFiles reports every file uploaded under name, for a field the client
// repeats. A request with no such file reports an [ErrBadRequest] that wraps
// [http.ErrMissingFile].
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

// MultipartForm reports the parsed multipart form. A body that is not
// multipart reports an [ErrBadRequest]. A part that spilled to a temporary
// file is removed once the request ends.
func (b *Base) MultipartForm() (*multipart.Form, error) {
	if err := b.parseForm(); err != nil {
		return nil, err
	}
	if b.req.MultipartForm == nil {
		return nil, ErrBadRequest.WithMessage("the request body is not a multipart form")
	}
	return b.req.MultipartForm, nil
}
