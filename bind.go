package router

import (
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
)

// DefaultMaxBodyBytes is the request body that the Bind methods read before
// they report [ErrPayloadTooLarge]. [Router.MaxBodyBytes] changes it, and
// [Base.SetBodyLimit] changes it for one request.
const DefaultMaxBodyBytes int64 = 4 << 20

const defaultMaxMultipartMemory int64 = 32 << 20

// Bind fills a T from the request and validates it. T is a struct or a
// pointer to one, and names the source of each field with a struct tag:
// `param` for a route parameter, `query`, `header`, and `json` or `form` for
// the body. A field without the tag of a source is never filled from it.
//
// Bind reads the body whenever the request carries one, that is when it has
// a Content-Length above zero, or a body of unknown length, whatever the
// method. A Content-Type without a body reads nothing. The
// Content-Type picks the decoder: JSON for application/json and any "+json"
// type, and a form for the two form types. JSON follows the rules of
// [Base.BindJSON]: a field with no tag at all takes the member of its Go
// name, and a field tagged for another source and not for `json` takes no
// member. The route parameters, the query and the headers come next, so a field
// tagged for both the body and one of them takes the value of the latter. A
// T that implements [Validator] is validated last.
//
// A value that does not fit its field is an [ErrBadRequest] "invalid
// request", and a value that Validate refuses is an [ErrUnprocessableEntity],
// unless the Validator names a status of its own. Both list the fields in
// Details, which [FieldErrorsOf] reads. The T comes back with the error and
// holds what decoded: every field of a form, the query, the path or the
// headers, and the JSON members before the one that failed.
// errors.AsType[*HTTPError] finds one in every error, and [StatusOf] reports
// its status, so a handler can return it as it stands.
//
// A path is the exception: [Base.BindPath] and [Base.ParamAs] report an
// [ErrNotFound] for a value that does not decode, because such a path names no
// resource. A T that is not a struct or a pointer to one is a bug in the
// caller, and every Bind method but BindJSON reports it as an
// [ErrInternalServerError].
func (b *Base) Bind[T any]() (T, error) {
	p, sv, err := newTarget[T]()
	if err != nil {
		return *p, err
	}
	if b.hasBody() {
		if err := b.decodeBody(p, sv); err != nil {
			return *p, err
		}
		// A JSON null leaves a pointer T nil, which validate refuses.
		if sv = targetStruct(p); !sv.IsValid() {
			return *p, validate(p)
		}
	}
	if err := b.decodePath(sv); err != nil {
		return *p, err
	}
	fields := decodeValues(b.queryValues(), sv, "query")
	fields = append(fields, decodeValues(url.Values(b.req.Header), sv, "header")...)
	if fields != nil {
		return *p, badFields(fields, joinFields(fields))
	}
	return *p, validate(p)
}

// hasBody reports whether the request carries a body: a Content-Length above
// zero, or an unknown length, -1, on a body that is there. A Content-Type
// alone does not count.
func (b *Base) hasBody() bool {
	switch n := b.req.ContentLength; {
	case n > 0:
		return true
	case n < 0:
		return b.req.Body != nil && b.req.Body != http.NoBody
	default:
		return false
	}
}

func (b *Base) decodeBody(dst any, sv reflect.Value) error {
	mt, params, err := b.mediaType()
	if err != nil {
		return ErrUnsupportedMediaType.WithMessage("malformed Content-Type").WithError(err)
	}
	switch {
	case mt == MIMEApplicationJSON || strings.HasSuffix(mt, "+json"):
		return b.decodeJSON(dst, nil)
	case mt == MIMEApplicationForm, mt == MIMEMultipartForm:
		if err := b.readForm(mt, params); err != nil {
			return err
		}
		return decodeInto(b.req.PostForm, sv, "form")
	case mt == "":
		return ErrUnsupportedMediaType.WithMessage("a %s body needs a Content-Type", b.req.Method)
	default:
		return ErrUnsupportedMediaType.WithMessage("cannot decode a %s body", mt)
	}
}

// mediaType parses the Content-Type of the request. A request without one
// reports "" and no error.
func (b *Base) mediaType() (string, map[string]string, error) {
	ct := b.req.Header.Get(HeaderContentType)
	if ct == "" {
		return "", nil, nil
	}
	return mime.ParseMediaType(ct)
}

// newTarget allocates the value a Bind method fills, and reports the struct
// inside it: the value itself for a struct T, and a new struct for a pointer
// T. Any other T reports an [ErrInternalServerError].
func newTarget[T any]() (*T, reflect.Value, error) {
	p := new(T)
	rv := reflect.ValueOf(p).Elem()
	if rv.Kind() == reflect.Pointer && rv.Type().Elem().Kind() == reflect.Struct {
		rv.Set(reflect.New(rv.Type().Elem()))
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return p, reflect.Value{}, ErrInternalServerError.WithError(
			fmt.Errorf("router: cannot bind into %s: T must be a struct or a pointer to one", rv.Type()))
	}
	return p, rv, nil
}

// targetStruct reports the struct that p holds, or the zero Value when p holds
// a nil pointer.
func targetStruct[T any](p *T) reflect.Value {
	rv := reflect.ValueOf(p).Elem()
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	return rv
}

// BindJSON decodes the body as JSON into a T and validates it. opts win over
// the options of [Router.JSONOptions]. It reports errors as [Base.Bind] does.
//
// A field with no tag takes the member of its Go name, as json/v2 does. A
// field that carries a `query`, `param`, `header` or `form` tag and no `json`
// tag takes no member, at any depth, so a client cannot set through the body
// what the struct takes from the request. A type that implements its own
// unmarshaling is left as it decoded itself.
//
// A member of the wrong JSON type, a value that its UnmarshalText or
// UnmarshalJSON rejects, and an unknown member under RejectUnknownMembers
// each report a 400 "invalid request" with one [FieldError] at the path of the
// member, such as events[3].amount. Decoding stops at the first member that
// does not fit, so T holds the members before it. A malformed body reports
// "malformed JSON body", and an empty body and a body over the limit each
// report their own [HTTPError]. The text of the decoder stays in Err.
//
// Options of the v1 legacy semantics make the decoder report errors of
// another type, so such a member reports "malformed JSON body" and no field.
func (b *Base) BindJSON[T any](opts ...json.Options) (T, error) {
	var v T
	if err := b.decodeJSON(&v, opts); err != nil {
		return v, err
	}
	return v, validate(&v)
}

func (b *Base) decodeJSON(dst any, opts []json.Options) error {
	body := countingBody{r: b.limitedBody()}
	err := json.UnmarshalRead(&body, dst, b.jsonOptions(opts)...)
	// Even a failed decode hands back what it filled, so the fields of the
	// other sources are cleared either way.
	clearOtherSources(reflect.ValueOf(dst))
	if err != nil {
		// json/v2 reports an empty body with the same syntax error as a
		// truncated one, so the byte count tells them apart.
		return jsonError(err, body.read == 0)
	}
	return nil
}

// jsonError keeps the text of json/v2 out of the message: it names Go types,
// and its wording changes from run to run.
func jsonError(err error, empty bool) *HTTPError {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return ErrPayloadTooLarge.WithError(err)
	}
	if empty {
		return ErrBadRequest.WithMessage("the request body is empty").WithError(err)
	}
	// A SemanticError can wrap a SyntacticError, so the syntax check goes
	// first.
	if _, ok := errors.AsType[*jsontext.SyntacticError](err); ok || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrBadRequest.WithMessage("malformed JSON body").WithError(err)
	}
	se, ok := errors.AsType[*json.SemanticError](err)
	if !ok {
		return ErrBadRequest.WithMessage("malformed JSON body").WithError(err)
	}
	if field := jsonField(se.JSONPointer); field != "" {
		return badFields([]FieldError{{Field: field, Message: jsonProblem(se)}}, err)
	}
	return ErrBadRequest.WithMessage("the request body %s", jsonProblem(se)).WithError(err)
}

func jsonProblem(se *json.SemanticError) string {
	switch {
	case errors.Is(se.Err, json.ErrUnknownName):
		return "is not a known field"
	case se.Err == nil:
		return "has the wrong JSON type"
	default:
		return "is not a valid value"
	}
}

// jsonField spells a JSON pointer the way a client reads a field: /events/3/amount
// becomes events[3].amount.
func jsonField(p jsontext.Pointer) string {
	var sb strings.Builder
	for tok := range p.Tokens() {
		if tok != "" && strings.Trim(tok, "0123456789") == "" {
			sb.WriteByte('[')
			sb.WriteString(tok)
			sb.WriteByte(']')
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(tok)
	}
	return sb.String()
}

// BindForm fills a T from the form of the body and validates it, through the
// `form` tag. It reads a URL-encoded form and a multipart one alike, for any
// method. Parsing happens once per request, so a later form read costs
// nothing. A bool field reads a checkbox: "on" when it is checked, and false
// when it is absent.
//
// It reports errors as [Base.Bind] does. Every field that decoded is set, and
// a field that failed keeps its zero value, nil for a pointer.
func (b *Base) BindForm[T any]() (T, error) {
	p, sv, err := newTarget[T]()
	if err != nil {
		return *p, err
	}
	if err := b.parseForm(); err != nil {
		return *p, err
	}
	if err := decodeInto(b.req.PostForm, sv, "form"); err != nil {
		return *p, err
	}
	return *p, validate(p)
}

// BindQuery fills a T from the query string and validates it, through the
// `query` tag. It reports errors as [Base.Bind] does.
func (b *Base) BindQuery[T any]() (T, error) {
	p, sv, err := newTarget[T]()
	if err != nil {
		return *p, err
	}
	if err := decodeInto(b.queryValues(), sv, "query"); err != nil {
		return *p, err
	}
	return *p, validate(p)
}

// BindPath fills a T from the route parameters and validates it, through the
// `param` tag. A field that does not decode reports an [ErrNotFound] with no
// Details, because such a path names no resource; [FieldErrorsOf] still finds
// the fields in its cause. Validation reports errors as [Base.Bind] does.
func (b *Base) BindPath[T any]() (T, error) {
	p, sv, err := newTarget[T]()
	if err != nil {
		return *p, err
	}
	if err := b.decodePath(sv); err != nil {
		return *p, err
	}
	return *p, validate(p)
}

func (b *Base) decodePath(sv reflect.Value) error {
	if len(b.paramNames) == 0 || len(structFields(sv.Type(), "param")) == 0 {
		return nil
	}
	vals := make(url.Values, len(b.paramNames))
	for i, n := range b.paramNames {
		if i < len(b.paramVals) {
			vals[n] = b.paramVals[i : i+1 : i+1]
		}
	}
	if fields := decodeValues(vals, sv, "param"); fields != nil {
		return ErrNotFound.WithError(joinFields(fields))
	}
	return nil
}

// BindHeader fills a T from the request headers and validates it, through the
// `header` tag, whose value is the header name in any case. It reports errors
// as [Base.Bind] does.
func (b *Base) BindHeader[T any]() (T, error) {
	p, sv, err := newTarget[T]()
	if err != nil {
		return *p, err
	}
	if err := decodeInto(url.Values(b.req.Header), sv, "header"); err != nil {
		return *p, err
	}
	return *p, validate(p)
}

// Validator is a bound value that checks itself. Every Bind method calls
// Validate once the value decoded in full, and never after a field failed to
// decode. What Validate returns decides the answer:
//
//   - An [HTTPError], wrapped or not, passes through as it stands, with its
//     status, message and Details.
//   - A [StatusCoder] passes through inside an HTTPError of its status. Its
//     text stays out of the response.
//   - A [FieldError], or several joined with [errors.Join], becomes an
//     [ErrUnprocessableEntity] that lists them.
//   - Any other error becomes a plain [ErrUnprocessableEntity], and its text
//     reaches only the log.
//
// The first HTTPError in the tree decides before a StatusCoder, as in
// [StatusOf], and FieldErrors beside it are not added to its Details. When
// its status is zero, the rest of the list decides. A typed nil *HTTPError is
// passed over, and a tree with no other becomes a plain
// ErrUnprocessableEntity.
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
	he, ok := httpErrorOf(err)
	if !ok {
		if _, isNil := errors.AsType[*HTTPError](err); isNil {
			// A typed nil has no fields to read, so it is not kept as the
			// cause.
			return ErrUnprocessableEntity
		}
	}
	// A status of zero would answer 500, so such an error falls through to
	// the 422 below.
	if ok && he.Status != 0 {
		return err
	}
	if sc, ok := errors.AsType[StatusCoder](err); ok && sc.StatusCode() != 0 {
		return NewHTTPError(sc.StatusCode(), "").WithError(err)
	}
	if fields := FieldErrorsOf(err); fields != nil {
		return ErrUnprocessableEntity.WithDetails(fields).WithError(err)
	}
	return ErrUnprocessableEntity.WithError(err)
}

// decodeInto fills sv from vals through tag, and reports the fields that
// failed as one [ErrBadRequest].
func decodeInto(vals url.Values, sv reflect.Value, tag string) error {
	if fields := decodeValues(vals, sv, tag); fields != nil {
		return badFields(fields, joinFields(fields))
	}
	return nil
}

func joinFields(fields []FieldError) error {
	errs := make([]error, len(fields))
	for i, f := range fields {
		errs[i] = f
	}
	return errors.Join(errs...)
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

// maxFormBytes caps a URL-encoded body when the router lifts its own cap, as
// net/http caps one it parses.
const maxFormBytes int64 = 10 << 20

// parseForm reads the form of the body once per request, for any method, into
// PostForm, and into MultipartForm as well for a multipart body. A body of
// another type leaves an empty PostForm and stays unread.
func (b *Base) parseForm() error {
	mt, params, err := b.mediaType()
	if err != nil {
		return b.setFormError(ErrBadRequest.WithMessage("malformed form body").WithError(err))
	}
	return b.readForm(mt, params)
}

// readForm is parseForm for a media type already parsed.
func (b *Base) readForm(mt string, params map[string]string) error {
	if err := b.formError(); err != nil {
		return err
	}
	if b.formRead(mt) {
		return nil
	}

	var err error
	switch mt {
	case MIMEMultipartForm:
		err = b.readMultipart(params["boundary"])
	case MIMEApplicationForm:
		err = b.readURLEncoded()
	default:
		b.req.PostForm = make(url.Values)
		return nil
	}
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return b.setFormError(ErrPayloadTooLarge.WithError(err))
	}
	return b.setFormError(ErrBadRequest.WithMessage("malformed form body").WithError(err))
}

// formRead reports whether the form is parsed already, by the router or by an
// earlier reader such as [http.Request.ParseForm]. That one reads the body
// only for POST, PUT and PATCH, and leaves an empty PostForm for any other
// method, which does not count.
func (b *Base) formRead(mt string) bool {
	if mt == MIMEMultipartForm {
		return b.req.MultipartForm != nil
	}
	if b.req.PostForm == nil {
		return false
	}
	switch b.req.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return len(b.req.PostForm) > 0 || mt != MIMEApplicationForm
}

func (b *Base) readURLEncoded() error {
	body := b.limitedBody()
	if b.bodyLimit() <= 0 {
		body = http.MaxBytesReader(innermostWriter(b.res), body, maxFormBytes)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	vals, err := url.ParseQuery(string(data))
	if err != nil {
		return err
	}
	b.req.PostForm = vals
	return nil
}

func (b *Base) readMultipart(boundary string) error {
	if boundary == "" {
		return http.ErrMissingBoundary
	}
	memory := b.opts().maxMultipart
	if memory <= 0 {
		memory = defaultMaxMultipartMemory
	}
	form, err := multipart.NewReader(b.limitedBody(), boundary).ReadForm(memory)
	if err != nil {
		return err
	}
	b.req.MultipartForm = form
	b.req.PostForm = url.Values(form.Value)
	if len(form.File) > 0 {
		d := b.deferrals()
		d.forms = append(d.forms, form)
	}
	return nil
}

// removeSpilledParts removes the temporary files of the multipart forms that b
// parsed. net/http removes those of the request it holds, but the router may
// parse into a copy that Decompress or a SetRequest made, so nothing else
// would. The router calls it once ServeHTTP is done with the request, and not
// when the request context ends, which can come while the handler still
// reads the files, or never.
func (b *Base) removeSpilledParts() {
	if b.deferred == nil || b.deferred.forms == nil {
		return
	}
	for _, form := range b.deferred.forms {
		//nolint:errcheck // A file that is gone already is the wanted outcome.
		form.RemoveAll()
	}
	b.deferred.forms = nil
}

// otherSources are the tags that name a source other than a JSON body.
var otherSources = [...]string{"query", "param", "header", "form"}

// ofOtherSource reports a field that another source fills and a JSON body must
// not: it carries a query, param, header or form tag, and no json tag. json/v2
// fills such a field from the member of its Go name, which would let a client
// set what the struct meant to take from a header or the path.
func ofOtherSource(f reflect.StructField) bool {
	if _, ok := f.Tag.Lookup("json"); ok {
		return false
	}
	for _, tag := range otherSources {
		if name, _, _ := strings.Cut(f.Tag.Get(tag), ","); name != "" && name != "-" {
			return true
		}
	}
	return false
}

// clearOtherSources zeroes, in the value v decoded from JSON, every field of
// another source, at any depth. A type that decodes itself is left alone.
func clearOtherSources(v reflect.Value) {
	if !v.IsValid() || !reachesOtherSource(v.Type()) {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			clearOtherSources(v.Elem())
		}
	case reflect.Struct:
		for f, fv := range v.Fields() {
			switch {
			case ofOtherSource(f):
				if fv.CanSet() {
					fv.SetZero()
				}
			// The exported fields of an unexported embedded struct are
			// still set through it, by json/v2 and by reflect alike.
			case f.IsExported() || f.Anonymous:
				clearOtherSources(fv)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			clearOtherSources(v.Index(i))
		}
	case reflect.Map:
		if !v.CanInterface() {
			return
		}
		for iter := v.MapRange(); iter.Next(); {
			elem := reflect.New(v.Type().Elem()).Elem()
			elem.Set(iter.Value())
			clearOtherSources(elem)
			v.SetMapIndex(iter.Key(), elem)
		}
	}
}

var reachCache sync.Map // reflect.Type -> bool

// reachesOtherSource reports whether a value of type t can hold a field of
// another source, so a value that cannot is not walked.
func reachesOtherSource(t reflect.Type) bool {
	if v, ok := reachCache.Load(t); ok {
		return v.(bool)
	}
	// Only the answer for the root is cached: the answer for a type met
	// inside a cycle is partial.
	reach := typeReaches(t, make(map[reflect.Type]bool))
	reachCache.Store(t, reach)
	return reach
}

func typeReaches(t reflect.Type, seen map[reflect.Type]bool) bool {
	if decodesItself(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return typeReaches(t.Elem(), seen)
	case reflect.Struct:
		if seen[t] {
			return false
		}
		seen[t] = true
		for f := range t.Fields() {
			if !f.IsExported() && !f.Anonymous {
				continue
			}
			if ofOtherSource(f) || typeReaches(f.Type, seen) {
				return true
			}
		}
	}
	return false
}

var (
	jsonUnmarshalerType     = reflect.TypeFor[json.Unmarshaler]()
	jsonUnmarshalerFromType = reflect.TypeFor[json.UnmarshalerFrom]()
	textUnmarshalerType     = reflect.TypeFor[encoding.TextUnmarshaler]()
)

func decodesItself(t reflect.Type) bool {
	if t.Kind() != reflect.Pointer {
		t = reflect.PointerTo(t)
	}
	return t.Implements(jsonUnmarshalerType) || t.Implements(jsonUnmarshalerFromType) ||
		t.Implements(textUnmarshalerType)
}

// ParamAs reads route parameter name, of the path or the host, as a T. See
// [ParseValue] for the types it takes.
//
// A value that does not parse names no resource and reports an [ErrNotFound]
// that wraps the parse error; a class in the pattern, such as "{id:int}",
// usually turns such a value away before the handler runs. A name the route
// does not have is a bug in the caller and reports an
// [ErrInternalServerError].
func (b *Base) ParamAs[T any](name string) (T, error) {
	raw, ok := b.param(name)
	if !ok {
		var v T
		return v, ErrInternalServerError.WithError(fmt.Errorf("router: the route %q has no parameter %q", b.RoutePattern(), name))
	}
	v, err := ParseValue[T](raw)
	if err != nil {
		return v, ErrNotFound.WithError(fmt.Errorf("route parameter %s: %w", name, err))
	}
	return v, nil
}

// ParamAsDefault reads route parameter name as a T, or reports def when the
// parameter is absent or empty, or does not parse.
func (b *Base) ParamAsDefault[T any](name string, def T) T {
	return valueOr(b.Param(name), def)
}

// QueryAs reads query parameter name as a T. See [ParseValue] for the types it
// takes. An absent or empty parameter and a value that does not parse each
// report an [ErrBadRequest]; see [Base.FormAs] for the first.
func (b *Base) QueryAs[T any](name string) (T, error) {
	return parseAs[T](b.Query(name), "query parameter", name)
}

// QueryAsDefault reads query parameter name as a T, or reports def when the
// parameter is absent or empty, or does not parse.
func (b *Base) QueryAsDefault[T any](name string, def T) T {
	return valueOr(b.Query(name), def)
}

// QueryAllAs reads every value of query parameter name as a T, for a parameter
// the client repeats. It reports a nil slice when the parameter is absent, and
// the zero value for an empty one.
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
// time.Duration or time.Time, a type that implements
// encoding.TextUnmarshaler, or a pointer to one of these. A bool takes what
// strconv.ParseBool takes, and also on and off. An empty s parses only as a
// string. The error is the parse failure itself, without a status, and a
// pointer T is nil on error.
func ParseValue[T any](s string) (T, error) {
	var v T
	if s == "" && indirectType(reflect.TypeFor[T]()).Kind() != reflect.String {
		return v, errors.New("the value is empty")
	}
	if err := setScalar(reflect.ValueOf(&v).Elem(), s, ""); err != nil {
		return v, err
	}
	return v, nil
}

// parseAs is ParseValue for a named value of the request, with an
// [ErrBadRequest] for a value that is absent, empty or malformed. An absent
// or empty value carries one [FieldError] for name, as [Base.Bind] reports
// one, so a form can show it beside the field.
func parseAs[T any](raw, kind, name string) (T, error) {
	if raw == "" {
		var v T
		fe := FieldError{Field: name, Message: "is required"}
		return v, ErrBadRequest.WithMessage("missing %s %q", kind, name).
			WithDetails([]FieldError{fe}).WithError(fe)
	}
	v, err := ParseValue[T](raw)
	if err != nil {
		return v, ErrBadRequest.WithMessage("%s %s: %s", kind, name, err).WithError(err)
	}
	return v, nil
}

func valueOr[T any](s string, def T) T {
	if s == "" {
		return def
	}
	if v, err := ParseValue[T](s); err == nil {
		return v
	}
	return def
}

// FormValue reads form field name from the body, or "" when the field is
// absent or the body does not parse. Unlike [http.Request.FormValue] it reads
// the body alone and never the query. Use [Base.FormValues] to see the parse
// error, and [Base.FormAs] for a field that must be there. The ParseForm
// middleware refuses a body that does not parse before the handler runs.
func (b *Base) FormValue(name string) string {
	//nolint:errcheck // The caller asked for a value, not for the parse error.
	b.parseForm()
	return b.req.PostForm.Get(name)
}

// FormAs reads form field name as a T. See [ParseValue] for the types it
// takes. A value that does not parse reports an [ErrBadRequest], and so does
// an absent or empty field, with one [FieldError] for name in its Details, so
// FormAs[string] reads a field that must be there. A body that does not parse
// reports what [Base.FormValues] reports.
func (b *Base) FormAs[T any](name string) (T, error) {
	if err := b.parseForm(); err != nil {
		var v T
		return v, err
	}
	return parseAs[T](b.req.PostForm.Get(name), "form field", name)
}

// FormAsDefault reads form field name as a T, or reports def when the field is
// absent or empty, does not parse, or the body does not parse.
func (b *Base) FormAsDefault[T any](name string, def T) T {
	return valueOr(b.FormValue(name), def)
}

// FormValues reports the parsed form of the body. The router parses it once
// per request and hands back the same map, so the caller must not change it.
func (b *Base) FormValues() (url.Values, error) {
	if err := b.parseForm(); err != nil {
		return nil, err
	}
	return b.req.PostForm, nil
}

// FormFile opens the first file uploaded under name. The caller closes the
// file. A request with no such file reports an [ErrBadRequest] that wraps
// [http.ErrMissingFile].
func (b *Base) FormFile(name string) (multipart.File, *multipart.FileHeader, error) {
	fhs, err := b.FormFiles(name)
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
// file is removed once the router is done with the request. On a Base from
// [NewBase], outside a router, the caller removes them with RemoveAll.
func (b *Base) MultipartForm() (*multipart.Form, error) {
	if err := b.parseForm(); err != nil {
		return nil, err
	}
	if b.req.MultipartForm == nil {
		return nil, ErrBadRequest.WithMessage("the request body is not a multipart form")
	}
	return b.req.MultipartForm, nil
}
