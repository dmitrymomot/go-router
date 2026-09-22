package router

import (
	"encoding"
	"fmt"
	"net/textproto"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// decodeValues fills the struct sv from vals, through the fields that carry
// tag. It keeps going past a field that fails, and reports every such field.
func decodeValues(vals url.Values, sv reflect.Value, tag string) []FieldError {
	if len(vals) == 0 || len(structFields(sv.Type(), tag)) == 0 {
		return nil
	}
	var fields []FieldError
	decodeStruct(vals, sv, tag, &fields, make(map[reflect.Type]bool))
	return fields
}

func decodeStruct(
	vals url.Values,
	rv reflect.Value,
	tag string,
	fields *[]FieldError,
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
			*fields = append(*fields, FieldError{Field: f.key, Message: err.Error()})
		}
	}
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
