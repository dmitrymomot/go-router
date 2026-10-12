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
// method. A Content-Type without a body reads nothing. The Content-Type picks
// the decoder: JSON for application/json and any "+json" type, a form for the
// two form types, and the [BodyDecoder] that [Router.Decoder] registered for
// any other type. JSON follows the rules of [Base.BindJSON]: a field with no
// tag at all takes the member of its Go name, a field tagged for another
// source and not for `json` takes no member, and member names match
// case-sensitively, so a member "Name" does not fill a field tagged
// `json:"name"` unless json.MatchCaseInsensitiveNames(true) is set. A JSON
// Content-Type with a charset other than UTF-8 reports an
// [ErrUnsupportedMediaType]. The route parameters, the query and the headers
// come next, so a field tagged for both the body and one of them takes the
// value of the latter. A T that implements [Validator] is validated last.
//
// The tags of the route parameters, the query, the headers and a form take
// these forms:
//
//   - `query:"ids,split"` on a slice splits each value at its commas, so
//     ids=1,2,3 fills []int{1, 2, 3}. Each part is trimmed of spaces, and an
//     empty part is dropped.
//   - `default:"20"` beside the tag fills a field whose key the request does
//     not carry at all, and that no other source filled. A key with an empty
//     value is not absent. A slice takes a default split at its commas.
//   - A struct field, or a pointer to one, in the query or a form reads the
//     keys under its own: `form:"addr"` on a struct whose City is tagged
//     `form:"city"` reads addr.city. A pointer is allocated only for a key
//     under it.
//   - A map[string]V field, where V is a value or a slice of them, in the
//     query or a form reads the keys in brackets: `query:"price"` reads
//     price[low]=1 into the key low.
//
// A field of a type that its source cannot fill, such as a chan, a func, a
// map with keys that are not strings, or a struct in a header, is a bug in T.
// So is a split option on a field that is not a slice, and a default that
// does not parse as its field. The plan of each type is built and checked
// once, at its first Bind, and every Bind of such a T reports an
// [ErrInternalServerError] whose cause names the field, whatever the request
// carries.
//
// A value that does not fit its field is an [ErrBadRequest] "invalid
// request", and a value that Validate refuses is an [ErrUnprocessableEntity],
// unless the Validator names a status of its own. Both list the fields in
// Details, which [FieldErrorsOf] reads. The message of a field that does not
// parse is a fixed phrase, such as "is not a valid integer", which repeats
// neither the value nor a Go type; the detail stays in the cause, for the
// log. The T comes back with the error and holds what decoded: every field of
// a form, the query, the path or the headers, and the JSON members before the
// one that failed. errors.AsType[*HTTPError] finds one in every error, and
// [StatusOf] reports its status, so a handler can return it as it stands.
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
	var query url.Values
	if planOf(sv.Type(), "query").has() {
		// A T without a query field leaves the query unparsed.
		query = b.queryValues()
	}
	fails, err := decodeValues(mapSource(query), sv, "query")
	if err != nil {
		return *p, ErrInternalServerError.WithError(err)
	}
	more, err := decodeValues(mapSource(b.req.Header), sv, "header")
	if err != nil {
		return *p, ErrInternalServerError.WithError(err)
	}
	if fails = append(fails, more...); fails != nil {
		return *p, badFields(fails)
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
	case isJSON(mt):
		if err := jsonCharset(params); err != nil {
			return err
		}
		return b.decodeJSON(dst, nil)
	case mt == MIMEApplicationForm, mt == MIMEMultipartForm:
		if err := b.readForm(mt, params); err != nil {
			return err
		}
		return decodeInto(b.req.PostForm, sv, "form")
	case mt == "":
		return ErrUnsupportedMediaType.WithMessage("a %s body needs a Content-Type", b.req.Method)
	default:
		if d := b.opts().decoders[mt]; d != nil {
			return b.decodeWith(d, dst, mt)
		}
		return ErrUnsupportedMediaType.WithMessage("cannot decode a %s body", clip(mt))
	}
}

// mediaType parses the Content-Type of the request. A request without one
// reports "" and no error.
func (b *Base) mediaType() (string, map[string]string, error) {
	ct := b.req.Header.Get(HeaderContentType)
	switch ct {
	case "":
		return "", nil, nil
	case MIMEApplicationJSON:
		// The common case needs no parse.
		return ct, nil, nil
	}
	return mime.ParseMediaType(ct)
}

// isJSON reports whether mt names JSON: application/json or any "+json" type.
func isJSON(mt string) bool {
	return mt == MIMEApplicationJSON || strings.HasSuffix(mt, "+json")
}

// jsonCharset refuses a JSON body in a charset other than UTF-8, the only one
// that JSON allows, rather than decode it wrong.
func jsonCharset(params map[string]string) error {
	if cs, ok := params["charset"]; ok && !strings.EqualFold(cs, "utf-8") {
		return ErrUnsupportedMediaType.WithMessage("a JSON body must be UTF-8")
	}
	return nil
}

// acceptJSON checks the Content-Type of a body that BindJSON decodes: none at
// all, or a JSON type in UTF-8.
func (b *Base) acceptJSON() error {
	mt, params, err := b.mediaType()
	switch {
	case err != nil:
		return ErrUnsupportedMediaType.WithMessage("malformed Content-Type").WithError(err)
	case mt == "":
		return nil
	case !isJSON(mt):
		return ErrUnsupportedMediaType.WithMessage("cannot decode a %s body as JSON", clip(mt))
	}
	return jsonCharset(params)
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
// A request without a Content-Type is decoded as JSON. A Content-Type other
// than application/json or a "+json" type, or a charset other than UTF-8,
// reports an [ErrUnsupportedMediaType]. So a cross-site form, which can send
// text/plain but not application/json, cannot drive a JSON endpoint that
// trusts a cookie.
//
// Member names match case-sensitively, as json/v2 matches them and unlike
// encoding/json: a member "Name" does not fill a field tagged `json:"name"`.
// Pass json.MatchCaseInsensitiveNames(true), here or to [Router.JSONOptions],
// to match in any case.
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
	if err := b.acceptJSON(); err != nil {
		return v, err
	}
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
	jsonClearer.clear(reflect.ValueOf(dst))
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
		return invalidRequest([]FieldError{{Field: field, Message: jsonProblem(se)}}, err)
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
	// A member name comes from the client, so a long one is cut.
	return clip(sb.String())
}

// BindForm fills a T from the form of the body and validates it, through the
// `form` tag in the forms that [Base.Bind] lists. It reads a URL-encoded form and a multipart one alike, for any
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
// `query` tag in the forms that [Base.Bind] lists. It reports errors as Bind
// does.
func (b *Base) BindQuery[T any]() (T, error) {
	p, sv, err := newTarget[T]()
	if err != nil {
		return *p, err
	}
	var query url.Values
	if planOf(sv.Type(), "query").has() {
		query = b.queryValues()
	}
	if err := decodeInto(query, sv, "query"); err != nil {
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
	if !planOf(sv.Type(), "param").has() {
		return nil
	}
	fails, err := decodeValues((*pathSource)(b), sv, "param")
	if err != nil {
		return ErrInternalServerError.WithError(err)
	}
	if fails != nil {
		_, cause := failed(fails)
		return ErrNotFound.WithError(cause)
	}
	return nil
}

// BindHeader fills a T from the request headers and validates it, through the
// `header` tag, whose value is the header name in any case. The split option
// and the default tag of [Base.Bind] apply. It reports errors as Bind does.
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
	// reflect.ValueOf(v) and any(v) box a pointer, which allocates nothing,
	// where any(*v) would copy a struct T to the heap. So *v is tried only
	// for a T that boxes for free.
	rv := reflect.ValueOf(v).Elem()
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return ErrBadRequest.WithMessage("the request body is null")
	}
	sv, ok := any(v).(Validator)
	if !ok && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
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
// failed as one [ErrBadRequest], or a struct that the source cannot fill as an
// [ErrInternalServerError].
func decodeInto(vals url.Values, sv reflect.Value, tag string) error {
	fails, err := decodeValues(mapSource(vals), sv, tag)
	if err != nil {
		return ErrInternalServerError.WithError(err)
	}
	if fails != nil {
		return badFields(fails)
	}
	return nil
}

// badFields is the 400 of a value that did not decode, one [FieldError] per
// field that failed.
func badFields(fails []fieldFailure) *HTTPError {
	fields, cause := failed(fails)
	return invalidRequest(fields, cause)
}

func invalidRequest(fields []FieldError, cause error) *HTTPError {
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

// otherSources are the tags that name a source other than the body.
var otherSources = [...]string{"query", "param", "header", "form"}

// ofOtherSource reports a field that another source fills and a body must
// not: it carries a query, param, header or form tag, and not the tag own of
// the body, such as json. json/v2 fills such a field from the member of its
// Go name, which would let a client set what the struct meant to take from a
// header or the path.
func ofOtherSource(f reflect.StructField, own string) bool {
	if _, ok := f.Tag.Lookup(own); ok {
		return false
	}
	for _, tag := range otherSources {
		if name, _, _ := strings.Cut(f.Tag.Get(tag), ","); name != "" && name != "-" {
			return true
		}
	}
	return false
}

// clearer zeroes the fields of another source in a value that a body decoded
// into, for a body whose fields carry the tag own.
type clearer struct {
	own   string
	plans sync.Map // reflect.Type -> *clearPlan
}

// jsonClearer is the clearer of a JSON body.
var jsonClearer = &clearer{own: "json"}

// clearPlan is what a clearer does to a type, built once per type.
type clearPlan struct {
	reach bool  // a value of the type can hold a field of another source
	zero  []int // the fields of another source, of a struct
	walk  []int // the fields of a struct that can hold one
}

func (c *clearer) plan(t reflect.Type) *clearPlan {
	if v, ok := c.plans.Load(t); ok {
		return v.(*clearPlan)
	}
	own := c.own
	p := &clearPlan{reach: typeReaches(t, own, make(map[reflect.Type]bool))}
	if p.reach && t.Kind() == reflect.Struct {
		for i := range t.NumField() {
			f := t.Field(i)
			switch {
			case ofOtherSource(f, own):
				p.zero = append(p.zero, i)
			// The exported fields of an unexported embedded struct are
			// still set through it, by json/v2 and by reflect alike.
			case (f.IsExported() || f.Anonymous) && c.plan(f.Type).reach:
				p.walk = append(p.walk, i)
			}
		}
	}
	v, _ := c.plans.LoadOrStore(t, p)
	return v.(*clearPlan)
}

// clear zeroes, in the value v that a body decoded into, every field of
// another source, at any depth. A type that decodes itself is left alone.
func (c *clearer) clear(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	p := c.plan(v.Type())
	if !p.reach {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			c.clear(v.Elem())
		}
	case reflect.Struct:
		for _, i := range p.zero {
			if fv := v.Field(i); fv.CanSet() {
				fv.SetZero()
			}
		}
		for _, i := range p.walk {
			c.clear(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			c.clear(v.Index(i))
		}
	case reflect.Map:
		if !v.CanInterface() {
			return
		}
		for iter := v.MapRange(); iter.Next(); {
			elem := reflect.New(v.Type().Elem()).Elem()
			elem.Set(iter.Value())
			c.clear(elem)
			v.SetMapIndex(iter.Key(), elem)
		}
	}
}

// typeReaches reports whether a value of type t can hold a field of another
// source, so a value that cannot is not walked. The answer for a type met
// inside a cycle is partial, so clearer.plan asks it of every type as a root.
func typeReaches(t reflect.Type, own string, seen map[reflect.Type]bool) bool {
	if decodesItself(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return typeReaches(t.Elem(), own, seen)
	case reflect.Struct:
		if seen[t] {
			return false
		}
		seen[t] = true
		for f := range t.Fields() {
			if !f.IsExported() && !f.Anonymous {
				continue
			}
			if ofOtherSource(f, own) || typeReaches(f.Type, own, seen) {
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
		return nil, malformed("query parameter", name, err)
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
		return v, malformed(kind, name, err)
	}
	return v, nil
}

// malformed is the error of a named value that does not parse: an
// [ErrBadRequest] whose message names the field and not the value, or an
// [ErrInternalServerError] for a type that ParseValue does not take.
func malformed(kind, name string, err error) *HTTPError {
	if _, ok := errors.AsType[*parseError](err); !ok {
		return ErrInternalServerError.WithError(err)
	}
	fe := FieldError{Field: name, Message: clientMessage(err)}
	return ErrBadRequest.WithMessage("%s %q %s", kind, name, fe.Message).
		WithDetails([]FieldError{fe}).WithError(err)
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
