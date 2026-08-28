package router

import (
	"encoding"
	"fmt"
	"net/textproto"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// decodeValues fills dst, which must be a non-nil pointer to a struct, from a
// set of URL values. It reads the field name from the named tag, then from the
// json tag, and falls back to the field name itself, its lower-case form and
// its canonical header form. With strict set it fills only the fields that a
// tag names.
//
// It handles strings, booleans, the integer and float kinds, [time.Duration],
// any type that implements [encoding.TextUnmarshaler] such as [time.Time],
// pointers to those, slices of those, and embedded structs. A time.Time field
// that carries a format tag reads that layout instead, and a byte slice takes
// the bytes of the value as they arrived.
//
// It keeps decoding after a value that does not fit and returns one
// [FieldError] per such value, so a form with three bad fields reports three.
// The error it returns names a target that is not a pointer to a struct, which
// is a fault of the caller and not of the request.
func decodeValues(vals url.Values, dst any, tag string, strict bool) ([]FieldError, error) {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, fmt.Errorf("decode target must be a non-nil pointer, got %T", dst)
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("decode target must point at a struct, got %s", rv.Kind())
	}
	var fields []FieldError
	decodeStruct(vals, rv, tag, strict, &fields)
	return fields, nil
}

// decodeStruct fills one struct and appends a [FieldError] for every value
// that did not fit.
func decodeStruct(vals url.Values, rv reflect.Value, tag string, strict bool, fields *[]FieldError) {
	for _, f := range structFields(rv.Type(), tag) {
		fv := rv.Field(f.index)

		if f.embedded {
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					if !fv.CanSet() {
						continue
					}
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				fv = fv.Elem()
			}
			decodeStruct(vals, fv, tag, strict, fields)
			continue
		}

		// Strict binding fills only what a tag names, so a request cannot reach
		// a field that the type never meant to expose.
		if strict && !f.tagged {
			continue
		}
		key, raw, ok := lookupValues(vals, f.keys)
		if !ok {
			continue
		}
		if err := setField(fv, raw, f.layout); err != nil {
			*fields = append(*fields, FieldError{Field: key, Message: err.Error()})
		}
	}
}

// fieldInfo is the decoding plan of one struct field.
type fieldInfo struct {
	// keys are the keys that the field reads, in the order that the decoder
	// tries them.
	keys []string

	// layout is the format tag of the field, empty without one.
	layout string

	// index is the position of the field in the struct.
	index int

	// tagged reports that a tag named the field.
	tagged bool

	// embedded reports an embedded struct whose own fields the decoder fills.
	embedded bool
}

// fieldsKey identifies a decoding plan. The tag belongs in the key because one
// type binds from a form, a query, a path and a header, and each of those
// reads a tag of its own.
type fieldsKey struct {
	typ reflect.Type
	tag string
}

// fieldCache holds the decoding plans, keyed by [fieldsKey].
var fieldCache sync.Map

// structFields returns the decoding plan of a struct type, and builds it on
// the first request that binds the type. The plan depends on the type and on
// the tag alone, so the decoder resolves the struct tags of a field once
// instead of re-parsing them on every request.
func structFields(rt reflect.Type, tag string) []fieldInfo {
	key := fieldsKey{typ: rt, tag: tag}
	if v, ok := fieldCache.Load(key); ok {
		plan, _ := v.([]fieldInfo)
		return plan
	}
	// Two requests that arrive together build the same plan, and the first one
	// to store it is the plan that both of them read.
	v, _ := fieldCache.LoadOrStore(key, buildFields(rt, tag))
	plan, _ := v.([]fieldInfo)
	return plan
}

// buildFields resolves the decoding plan of every field that the decoder
// fills, and leaves out the fields that it skips.
func buildFields(rt reflect.Type, tag string) []fieldInfo {
	plan := make([]fieldInfo, 0, rt.NumField())
	for i := range rt.NumField() {
		ft := rt.Field(i)
		name, layout, tagged, skip := fieldName(ft, tag)
		if skip {
			continue
		}
		embedded := ft.Anonymous && !tagged && indirectType(ft.Type).Kind() == reflect.Struct

		// An embedded struct promotes its exported fields even when its own
		// type name is unexported, and reflect can set those fields. Any other
		// unexported field it cannot set at all.
		if !ft.IsExported() && !embedded {
			continue
		}

		f := fieldInfo{layout: layout, index: i, tagged: tagged, embedded: embedded}
		if !embedded {
			// An embedded struct is not a value of its own, so it reads no key.
			f.keys = fieldKeys(name, ft.Name)
		}
		plan = append(plan, f)
	}
	return plan
}

// fieldKeys returns the keys that a field reads, in the order that the decoder
// tries them: the name that a tag gave it and then the name of the field
// itself, each of them as written, in lower case, and in the canonical header
// form. Resolving them once per type is what keeps the decoder from lowering a
// name on every request that leaves the field out.
func fieldKeys(name, goName string) []string {
	keys := make([]string, 0, 6)
	add := func(key string) {
		if key != "" && !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	for _, n := range []string{name, goName} {
		if n == "" {
			continue
		}
		add(n)
		add(strings.ToLower(n))
		// A header map holds X-Request-Id, and the tag that names it reads
		// x-request-id.
		add(textproto.CanonicalMIMEHeaderKey(n))
	}
	return keys
}

// fieldName returns the key that a field reads and the layout of its format
// tag. tagged reports whether a tag named the field, and skip reports a "-"
// tag.
func fieldName(ft reflect.StructField, tag string) (name, layout string, tagged, skip bool) {
	layout = ft.Tag.Get("format")
	for _, key := range []string{tag, "json"} {
		v, ok := ft.Tag.Lookup(key)
		if !ok {
			continue
		}
		v, _, _ = strings.Cut(v, ",")
		if v == "-" {
			return "", layout, true, true
		}
		if v != "" {
			return v, layout, true, false
		}
	}
	return ft.Name, layout, false, false
}

// lookupValues finds the first key that the values hold. It returns the key
// that matched, which is how the request spells the field.
func lookupValues(vals url.Values, keys []string) (string, []string, bool) {
	for _, k := range keys {
		if v, ok := vals[k]; ok {
			return k, v, true
		}
	}
	return "", nil, false
}

// indirectType returns the type that t holds, through as many pointers as it
// takes to reach a value.
func indirectType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// setField assigns one or more raw values to a field. layout is the format tag
// of the field, which only a [time.Time] reads.
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

// setScalar parses s into a single value. layout is the format tag of the
// field, empty for a field that carries none.
func setScalar(fv reflect.Value, s, layout string) error {
	// An empty value leaves a non-string field at its zero value, so that
	// "?limit=" does not fail. It comes before the pointer branch, because the
	// zero value of a pointer is nil: a cleared input leaves the field nil,
	// which is how a handler tells it from a zero that the client sent.
	if s == "" && indirectType(fv.Type()).Kind() != reflect.String {
		return nil
	}

	if fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return setScalar(fv.Elem(), s, layout)
	}

	// A layout beats the TextUnmarshaler of time.Time, which reads RFC 3339 and
	// nothing else, while an <input type="date"> posts a bare date.
	if layout != "" && fv.Type() == reflect.TypeFor[time.Time]() {
		return setTime(fv, s, layout)
	}

	if fv.CanAddr() {
		if u, ok := reflect.TypeAssert[encoding.TextUnmarshaler](fv.Addr()); ok {
			if err := u.UnmarshalText([]byte(s)); err != nil {
				return fmt.Errorf("cannot parse %q as %s: %w", s, fv.Type(), err)
			}
			return nil
		}
	}

	// A byte slice takes the bytes of the value itself. setField keeps such a
	// field out of the per-element path, because a request that names it once
	// sends one string and not a list of numbers.
	if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.Uint8 {
		fv.SetBytes([]byte(s))
		return nil
	}

	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		v, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("cannot parse %q as a boolean", s)
		}
		fv.SetBool(v)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if fv.Type() == reflect.TypeFor[time.Duration]() {
			d, err := time.ParseDuration(s)
			if err != nil {
				return fmt.Errorf("cannot parse %q as a duration", s)
			}
			fv.SetInt(int64(d))
			return nil
		}
		v, err := strconv.ParseInt(s, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("cannot parse %q as %s", s, fv.Type())
		}
		fv.SetInt(v)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := strconv.ParseUint(s, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("cannot parse %q as %s", s, fv.Type())
		}
		fv.SetUint(v)
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(s, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("cannot parse %q as %s", s, fv.Type())
		}
		fv.SetFloat(v)
	default:
		return fmt.Errorf("cannot decode a value into %s", fv.Type())
	}
	return nil
}

// setTime parses s into a [time.Time] with the layout that a format tag names.
// The layout is a reference time, the one [time.Parse] reads, or one of
// "unix", "unixmilli" and "unixnano" for a count since the epoch.
func setTime(fv reflect.Value, s, layout string) error {
	var t time.Time
	switch layout {
	case "unix", "unixmilli", "unixnano":
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("cannot parse %q as a %s timestamp", s, layout)
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
			return fmt.Errorf("cannot parse %q in the %s layout", s, layout)
		}
	}
	fv.Set(reflect.ValueOf(t))
	return nil
}
