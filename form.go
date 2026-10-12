package router

import (
	"encoding"
	"errors"
	"fmt"
	"net/textproto"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// decodeValues fills the struct sv from vals, through the fields that carry
// tag. It keeps going past a field that fails, and reports every such field.
func decodeValues(vals url.Values, sv reflect.Value, tag string) []fieldFailure {
	if len(vals) == 0 || len(structFields(sv.Type(), tag)) == 0 {
		return nil
	}
	var fields []fieldFailure
	decodeStruct(vals, sv, tag, &fields, make(map[reflect.Type]bool))
	return fields
}

func decodeStruct(
	vals url.Values,
	rv reflect.Value,
	tag string,
	fields *[]fieldFailure,
	active map[reflect.Type]bool,
) {
	rt := rv.Type()
	if active[rt] {
		return
	}
	active[rt] = true
	defer delete(active, rt)

	for _, f := range structFields(rt, tag) {
		fv := rv.Field(f.index)

		if f.embedded {
			ft := indirectType(fv.Type())
			if active[ft] {
				continue
			}
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					if !fv.CanSet() || !structHasValue(vals, ft, tag, active) {
						continue
					}
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				fv = fv.Elem()
			}
			decodeStruct(vals, fv, tag, fields, active)
			continue
		}

		raw, ok := vals[f.key]
		if !ok {
			continue
		}
		if err := setField(fv, raw, f.layout); err != nil {
			*fields = append(*fields, failure(f.key, err))
		}
	}
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

func structHasValue(vals url.Values, rt reflect.Type, tag string, active map[reflect.Type]bool) bool {
	if active[rt] {
		return false
	}
	active[rt] = true
	defer delete(active, rt)

	for _, f := range structFields(rt, tag) {
		if f.embedded {
			if structHasValue(vals, indirectType(rt.Field(f.index).Type), tag, active) {
				return true
			}
			continue
		}
		if _, ok := vals[f.key]; ok {
			return true
		}
	}
	return false
}

// fieldInfo is one field of a struct that a source fills. An embedded field
// has no key: its own fields are read in its place.
type fieldInfo struct {
	key      string
	layout   string
	index    int
	embedded bool
}

type fieldsKey struct {
	typ reflect.Type
	tag string
}

var fieldCache sync.Map

func structFields(rt reflect.Type, tag string) []fieldInfo {
	key := fieldsKey{typ: rt, tag: tag}
	if v, ok := fieldCache.Load(key); ok {
		plan, _ := v.([]fieldInfo)
		return plan
	}
	v, _ := fieldCache.LoadOrStore(key, buildFields(rt, tag))
	plan, _ := v.([]fieldInfo)
	return plan
}

// buildFields lists the fields of rt that tag names. A field without tag is
// left alone, whatever its Go name or its other tags, so a client cannot
// reach a field the struct never offered to its source. An untagged embedded
// struct lends its fields to the outer one.
func buildFields(rt reflect.Type, tag string) []fieldInfo {
	plan := make([]fieldInfo, 0, rt.NumField())
	for i := range rt.NumField() {
		ft := rt.Field(i)
		v, tagged := ft.Tag.Lookup(tag)
		name, _, _ := strings.Cut(v, ",")
		switch {
		case v == "-":
			continue
		case !tagged || name == "":
			if ft.Anonymous && indirectType(ft.Type).Kind() == reflect.Struct {
				plan = append(plan, fieldInfo{index: i, embedded: true})
			}
			continue
		case !ft.IsExported():
			continue
		}
		if tag == "header" {
			name = textproto.CanonicalMIMEHeaderKey(name)
		}
		plan = append(plan, fieldInfo{key: name, layout: ft.Tag.Get("format"), index: i})
	}
	return plan
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
