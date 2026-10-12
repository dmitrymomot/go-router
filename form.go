package router

import (
	"encoding"
	"errors"
	"fmt"
	"iter"
	"maps"
	"net/textproto"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// decodeValues fills the struct sv from vals, through the fields that carry
// tag. It keeps going past a field that fails, and reports every such field.
// The error is a struct that the source cannot fill, which is a bug in the
// caller: a field of a type that takes no value, or a default that does not
// parse.
func decodeValues(vals valueSource, sv reflect.Value, tag string) ([]fieldFailure, error) {
	plan := planOf(sv.Type(), tag)
	if plan.err != nil {
		return nil, plan.err
	}
	if len(plan.fields) == 0 || vals.size() == 0 && !plan.defaults {
		return nil, nil
	}
	d := valueDecoder{vals: vals, tag: tag}
	d.decodeStruct(sv, plan, "", nil)
	return d.fails, d.err
}

// valueSource is where a source keeps its values: [url.Values] for the query,
// a form and the headers, and the route parameters of a [Base] for the path,
// which so need no map of their own.
type valueSource interface {
	lookup(key string) ([]string, bool)
	keys() iter.Seq[string]
	size() int
}

// mapSource is the valueSource of a url.Values.
type mapSource url.Values

func (m mapSource) lookup(key string) ([]string, bool) { v, ok := m[key]; return v, ok }
func (m mapSource) keys() iter.Seq[string]             { return maps.Keys(m) }
func (m mapSource) size() int                          { return len(m) }

// pathSource is the valueSource of the route parameters of a Base.
type pathSource Base

func (p *pathSource) lookup(key string) ([]string, bool) {
	for i, n := range p.paramNames {
		if n == key && i < len(p.paramVals) {
			return p.paramVals[i : i+1 : i+1], true
		}
	}
	return nil, false
}

func (p *pathSource) keys() iter.Seq[string] { return slices.Values(p.paramNames) }
func (p *pathSource) size() int              { return len(p.paramNames) }

// valueDecoder fills a struct from the values of one source.
type valueDecoder struct {
	vals  valueSource
	tag   string
	fails []fieldFailure
	err   error
}

// decodeStruct fills rv through plan, under prefix: "" at the top, and
// "addr." inside a struct field keyed addr. active lists the structs that
// embed rv, so an embedding cycle stops; it stays nil, and allocates
// nothing, until a struct embeds another.
func (d *valueDecoder) decodeStruct(rv reflect.Value, plan *structPlan, prefix string, active []reflect.Type) {
	if plan.err != nil {
		// A type met first inside a cycle may carry the error of its own.
		d.err = plan.err
		return
	}
	rt := rv.Type()
	for i := range plan.fields {
		f := &plan.fields[i]
		fv := rv.Field(f.index)
		switch f.kind {
		case kindEmbedded:
			ft := indirectType(fv.Type())
			if ft == rt || slices.Contains(active, ft) {
				continue
			}
			sub := planOf(ft, d.tag)
			inner := append(active, rt)
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					if !fv.CanSet() || !d.hasValue(ft, sub, prefix, inner) {
						continue
					}
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				fv = fv.Elem()
			}
			d.decodeStruct(fv, sub, prefix, inner)
		case kindStruct:
			key := prefix + f.key + "."
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					// A struct of its own comes to life only for a key under
					// it, so a recursive type ends with the keys.
					if !d.hasPrefix(key) {
						continue
					}
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				fv = fv.Elem()
			}
			d.decodeStruct(fv, planOf(fv.Type(), d.tag), key, nil)
		case kindMap:
			d.decodeMap(fv, f, prefix+f.key)
		default:
			key := f.key
			if prefix != "" {
				key = prefix + f.key
			}
			raw, ok := d.vals.lookup(key)
			if !ok {
				if f.def != nil && fv.IsZero() {
					// buildPlan parsed the default already, so it cannot fail.
					_ = setField(fv, f.def, f.layout)
				}
				continue
			}
			if f.split {
				raw = splitValues(raw)
			}
			if err := setField(fv, raw, f.layout); err != nil {
				d.fails = append(d.fails, failure(key, err))
			}
		}
	}
}

// decodeMap fills a map field from the keys name[k], in the order of k.
func (d *valueDecoder) decodeMap(fv reflect.Value, f *fieldInfo, name string) {
	var keys []string
	for k := range d.vals.keys() {
		if _, ok := mapKey(k, name); ok {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return
	}
	slices.Sort(keys)
	mt := fv.Type()
	if fv.IsNil() {
		fv.Set(reflect.MakeMapWithSize(mt, len(keys)))
	}
	for _, k := range keys {
		raw, _ := d.vals.lookup(k)
		if f.split {
			raw = splitValues(raw)
		}
		ev := reflect.New(mt.Elem()).Elem()
		if err := setField(ev, raw, f.layout); err != nil {
			// The key comes from the client, so a long one is cut.
			d.fails = append(d.fails, failure(clip(k), err))
			continue
		}
		mk, _ := mapKey(k, name)
		fv.SetMapIndex(reflect.ValueOf(mk).Convert(mt.Key()), ev)
	}
}

// mapKey reports k of a key name[k]. A k with a bracket of its own does not
// count.
func mapKey(key, name string) (string, bool) {
	if len(key) < len(name)+2 || key[:len(name)] != name || key[len(name)] != '[' || key[len(key)-1] != ']' {
		return "", false
	}
	k := key[len(name)+1 : len(key)-1]
	if strings.ContainsAny(k, "[]") {
		return "", false
	}
	return k, true
}

// hasValue reports whether vals holds a value for a field of the struct rt,
// so an embedded pointer is not allocated for nothing.
func (d *valueDecoder) hasValue(rt reflect.Type, plan *structPlan, prefix string, active []reflect.Type) bool {
	for i := range plan.fields {
		f := &plan.fields[i]
		switch f.kind {
		case kindEmbedded:
			ft := indirectType(rt.Field(f.index).Type)
			if ft == rt || slices.Contains(active, ft) {
				continue
			}
			if d.hasValue(ft, planOf(ft, d.tag), prefix, append(active, rt)) {
				return true
			}
		case kindStruct:
			if d.hasPrefix(prefix + f.key + ".") {
				return true
			}
		case kindMap:
			if d.hasPrefix(prefix + f.key + "[") {
				return true
			}
		default:
			if _, ok := d.vals.lookup(prefix + f.key); ok {
				return true
			}
		}
	}
	return false
}

func (d *valueDecoder) hasPrefix(prefix string) bool {
	for k := range d.vals.keys() {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// splitValues splits each value at its commas, trims the spaces around each
// part, and drops the parts left empty.
func splitValues(raw []string) []string {
	var out []string
	for _, s := range raw {
		for part := range strings.SplitSeq(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// fieldFailure is a field that did not decode: the [FieldError] that the
// client reads, and the error behind it, which reaches only the log.
type fieldFailure struct {
	FieldError
	err error
}

func failure(field string, err error) fieldFailure {
	return fieldFailure{FieldError{Field: field, Message: clientMessage(err)}, err}
}

func (f fieldFailure) Error() string   { return f.Field + ": " + f.err.Error() }
func (f fieldFailure) Unwrap() []error { return []error{f.FieldError, f.err} }

// failed reports the [FieldError] of each failure, and their causes joined.
func failed(fails []fieldFailure) ([]FieldError, error) {
	fields := make([]FieldError, len(fails))
	errs := make([]error, len(fails))
	for i, f := range fails {
		fields[i] = f.FieldError
		errs[i] = f
	}
	return fields, errors.Join(errs...)
}

// fieldKind is how a source fills a field.
type fieldKind uint8

const (
	kindValue    fieldKind = iota // one value
	kindSlice                     // a slice, one element per value
	kindStruct                    // a struct of its own, from the keys key.name
	kindMap                       // a map[string]V, from the keys key[k]
	kindEmbedded                  // an untagged embedded struct, whose fields count as the outer one's
)

// fieldInfo is one field of a struct that a source fills.
type fieldInfo struct {
	key    string
	layout string
	def    []string // the values of the default tag, nil without one
	index  int
	kind   fieldKind
	split  bool
}

// structPlan is how a source fills a struct type, built once per type and
// tag.
type structPlan struct {
	fields []fieldInfo
	// err is a field that the source cannot fill, or a default that does not
	// parse, at any depth.
	err error
	// defaults reports a default in a field that is filled even without a
	// value, so an empty source still has work to do.
	defaults bool
}

// has reports whether the source fills any field.
func (p *structPlan) has() bool { return len(p.fields) > 0 || p.err != nil }

// The sources that decodeValues fills, one plan each per type.
const (
	tagQuery = iota
	tagParam
	tagHeader
	tagForm
	numTags
)

func tagIndex(tag string) int {
	switch tag {
	case "query":
		return tagQuery
	case "param":
		return tagParam
	case "header":
		return tagHeader
	case "form":
		return tagForm
	}
	panic("router: no source tag " + tag)
}

type typePlans [numTags]atomic.Pointer[structPlan]

// planCache maps a reflect.Type, which boxes into an any for free, to its
// *typePlans.
var planCache sync.Map

// planOf reports the plan of the struct rt for tag, and builds it on first
// use.
func planOf(rt reflect.Type, tag string) *structPlan {
	return planBuilding(rt, tag, nil)
}

func planBuilding(rt reflect.Type, tag string, building map[reflect.Type]bool) *structPlan {
	v, ok := planCache.Load(rt)
	if !ok {
		v, _ = planCache.LoadOrStore(rt, new(typePlans))
	}
	slot := &v.(*typePlans)[tagIndex(tag)]
	if p := slot.Load(); p != nil {
		return p
	}
	if building == nil {
		building = make(map[reflect.Type]bool)
	}
	building[rt] = true
	p := buildPlan(rt, tag, building)
	delete(building, rt)
	slot.CompareAndSwap(nil, p)
	return slot.Load()
}

// buildPlan lists the fields of rt that tag names. A field without tag is
// left alone, whatever its Go name or its other tags, so a client cannot
// reach a field the struct never offered to its source. An untagged embedded
// struct lends its fields to the outer one.
func buildPlan(rt reflect.Type, tag string, building map[reflect.Type]bool) *structPlan {
	p := &structPlan{fields: make([]fieldInfo, 0, rt.NumField())}
	fail := func(err error) {
		if p.err == nil {
			p.err = err
		}
	}
	// child takes in the plan of a struct inside rt. A struct that is
	// being built already, up a cycle, adds nothing new.
	child := func(t reflect.Type, always bool) {
		if building[t] {
			return
		}
		cp := planBuilding(t, tag, building)
		fail(cp.err)
		if always && cp.defaults {
			p.defaults = true
		}
	}
	for i := range rt.NumField() {
		sf := rt.Field(i)
		v, tagged := sf.Tag.Lookup(tag)
		name, opts, _ := strings.Cut(v, ",")
		switch {
		case v == "-":
			continue
		case !tagged || name == "":
			if sf.Anonymous && indirectType(sf.Type).Kind() == reflect.Struct {
				child(indirectType(sf.Type), sf.Type.Kind() == reflect.Struct)
				p.fields = append(p.fields, fieldInfo{index: i, kind: kindEmbedded})
			}
			continue
		case !sf.IsExported():
			continue
		}
		if tag == "header" {
			name = textproto.CanonicalMIMEHeaderKey(name)
		}
		f := fieldInfo{key: name, layout: sf.Tag.Get("format"), index: i}
		for opt := range strings.SplitSeq(opts, ",") {
			if opt == "split" {
				f.split = true
			}
		}
		where := fmt.Sprintf("router: field %s of %s, tagged %s:%q,", sf.Name, rt, tag, v)
		ft := sf.Type
		nests := tag == "query" || tag == "form"
		switch {
		case isScalar(ft):
			f.kind = kindValue
		case ft.Kind() == reflect.Slice && isScalar(ft.Elem()):
			f.kind = kindSlice
		case nests && ft.Kind() == reflect.Struct:
			f.kind = kindStruct
			child(ft, true)
		case nests && ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Struct:
			f.kind = kindStruct
			child(ft.Elem(), false)
		case nests && ft.Kind() == reflect.Map && ft.Key().Kind() == reflect.String &&
			(isScalar(ft.Elem()) || ft.Elem().Kind() == reflect.Slice && isScalar(ft.Elem().Elem())):
			f.kind = kindMap
		default:
			fail(fmt.Errorf("%s has a type that the %s source cannot fill: %s", where, tag, ft))
			continue
		}
		if f.split && f.kind != kindSlice && (f.kind != kindMap || ft.Elem().Kind() != reflect.Slice) {
			fail(fmt.Errorf("%s takes the split option, which needs a slice", where))
		}
		if def, ok := sf.Tag.Lookup("default"); ok {
			if err := checkDefault(&f, ft, def); err != nil {
				fail(fmt.Errorf("%s %w", where, err))
			}
			p.defaults = true
		}
		p.fields = append(p.fields, f)
	}
	return p
}

// checkDefault parses the default tag of f once, so a default that does not
// fit its field fails at the first Bind of the type and never later. A slice
// takes a default split at its commas.
func checkDefault(f *fieldInfo, ft reflect.Type, def string) error {
	switch f.kind {
	case kindValue:
		f.def = []string{def}
	case kindSlice:
		f.def = splitValues([]string{def})
	default:
		return errors.New("takes a default, which needs a value or a slice")
	}
	if err := setField(reflect.New(ft).Elem(), f.def, f.layout); err != nil {
		return fmt.Errorf("has a default %q that does not parse as %s: %w", clip(def), ft, err)
	}
	return nil
}

// isScalar reports whether setScalar fills a value of type t.
func isScalar(t reflect.Type) bool {
	t = indirectType(t)
	if t == timeType || reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.Uint8
	}
	return false
}

func indirectType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func setField(fv reflect.Value, raw []string, layout string) error {
	if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() != reflect.Uint8 {
		out := reflect.MakeSlice(fv.Type(), len(raw), len(raw))
		for i, s := range raw {
			if err := setScalar(out.Index(i), s, layout); err != nil {
				return err
			}
		}
		fv.Set(out)
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	return setScalar(fv, raw[0], layout)
}

func setScalar(fv reflect.Value, s, layout string) error {
	if s == "" && indirectType(fv.Type()).Kind() != reflect.String {
		return nil
	}

	if fv.Kind() == reflect.Pointer {
		if !fv.IsNil() {
			return setScalar(fv.Elem(), s, layout)
		}
		// Fill a new value before setting it, so a failure leaves the pointer
		// nil and not pointing at a zero.
		nv := reflect.New(fv.Type().Elem())
		if err := setScalar(nv.Elem(), s, layout); err != nil {
			return err
		}
		fv.Set(nv)
		return nil
	}

	if layout != "" && fv.Type() == timeType {
		return setTime(fv, s, layout)
	}

	if fv.CanAddr() {
		if u, ok := reflect.TypeAssert[encoding.TextUnmarshaler](fv.Addr()); ok {
			if err := u.UnmarshalText([]byte(s)); err != nil {
				msg := "is not a valid value"
				if fv.Type() == timeType {
					msg = "is not a valid time"
				}
				return parseFailure(msg, s, fv.Type(), clippedError{err})
			}
			return nil
		}
	}

	if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.Uint8 {
		fv.SetBytes([]byte(s))
		return nil
	}

	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		v, err := parseBool(s)
		if err != nil {
			return parseFailure("is not a valid boolean", s, fv.Type(), numErr(err))
		}
		fv.SetBool(v)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if fv.Type() == durationType {
			d, err := time.ParseDuration(s)
			if err != nil {
				return parseFailure("is not a valid duration", s, fv.Type(), nil)
			}
			fv.SetInt(int64(d))
			return nil
		}
		v, err := strconv.ParseInt(s, 10, fv.Type().Bits())
		if err != nil {
			return parseFailure("is not a valid integer", s, fv.Type(), numErr(err))
		}
		fv.SetInt(v)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := strconv.ParseUint(s, 10, fv.Type().Bits())
		if err != nil {
			return parseFailure("is not a valid integer", s, fv.Type(), numErr(err))
		}
		fv.SetUint(v)
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(s, fv.Type().Bits())
		if err != nil {
			return parseFailure("is not a valid number", s, fv.Type(), numErr(err))
		}
		fv.SetFloat(v)
	default:
		return fmt.Errorf("router: cannot decode a value into %s", fv.Type())
	}
	return nil
}

var (
	timeType     = reflect.TypeFor[time.Time]()
	durationType = reflect.TypeFor[time.Duration]()
)

// parseError is a value that does not parse. Msg is the fixed phrase a client
// reads, which names neither the input nor a Go type. Error holds the detail
// for the log, with the input cut to [maxEchoBytes].
type parseError struct {
	msg    string
	detail string
	err    error
}

func (e *parseError) Error() string { return e.detail }
func (e *parseError) Unwrap() error { return e.err }

func parseFailure(msg, s string, t reflect.Type, err error) *parseError {
	detail := fmt.Sprintf("cannot parse %q as %s", clip(s), t)
	if err != nil {
		detail += ": " + err.Error()
	}
	return &parseError{msg: msg, detail: detail, err: err}
}

// clientMessage is the phrase that a client reads for err, which setScalar
// reported.
func clientMessage(err error) string {
	if pe, ok := errors.AsType[*parseError](err); ok {
		return pe.msg
	}
	return "is not a valid value"
}

// numErr drops the input that a [strconv.NumError] repeats, since the detail
// of parseFailure names it already, cut.
func numErr(err error) error {
	if ne, ok := errors.AsType[*strconv.NumError](err); ok {
		return ne.Err
	}
	return err
}

// maxEchoBytes caps the input that an error repeats, so a long value makes
// neither a long log line nor a long answer.
const maxEchoBytes = 64

// clip cuts s to maxEchoBytes, at a rune boundary, and marks the cut.
func clip(s string) string {
	if len(s) <= maxEchoBytes {
		return s
	}
	i := maxEchoBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "…"
}

// clippedError cuts the text of an error that may repeat its input, such as
// the one of an UnmarshalText, and keeps the error itself for errors.Is.
type clippedError struct{ err error }

func (e clippedError) Error() string {
	const maxText = 4 * maxEchoBytes
	s := e.err.Error()
	if len(s) <= maxText {
		return s
	}
	i := maxText
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "…"
}

func (e clippedError) Unwrap() error { return e.err }

// parseBool is [strconv.ParseBool] that also reads on and off, in the same
// three casings. A checkbox with no value attribute sends "on".
func parseBool(s string) (bool, error) {
	switch s {
	case "on", "On", "ON":
		return true, nil
	case "off", "Off", "OFF":
		return false, nil
	}
	return strconv.ParseBool(s)
}

func setTime(fv reflect.Value, s, layout string) error {
	var t time.Time
	switch layout {
	case "unix", "unixmilli", "unixnano":
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return &parseError{
				msg:    "is not a valid time",
				detail: fmt.Sprintf("cannot parse %q as a %s timestamp", clip(s), layout),
				err:    numErr(err),
			}
		}
		switch layout {
		case "unix":
			t = time.Unix(n, 0)
		case "unixmilli":
			t = time.UnixMilli(n)
		case "unixnano":
			t = time.Unix(0, n)
		}
	default:
		var err error
		if t, err = time.Parse(layout, s); err != nil {
			return &parseError{
				msg:    "is not a valid time",
				detail: fmt.Sprintf("cannot parse %q in the %s layout", clip(s), layout),
			}
		}
	}
	fv.Set(reflect.ValueOf(t))
	return nil
}
