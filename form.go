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
	decodeStruct(vals, rv, tag, strict, &fields, make(map[reflect.Type]bool))
	return fields, nil
}

func decodeStruct(
	vals url.Values,
	rv reflect.Value,
	tag string,
	strict bool,
	fields *[]FieldError,
	active map[reflect.Type]bool,
) {
	rt := rv.Type()
	if active[rt] {
		return
	}
	active[rt] = true
	defer delete(active, rt)

	for _, f := range structFields(rv.Type(), tag) {
		fv := rv.Field(f.index)

		if f.embedded {
			ft := indirectType(fv.Type())
			if active[ft] {
				continue
			}
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					if !fv.CanSet() || !structHasValue(vals, ft, tag, strict, active) {
						continue
					}
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				fv = fv.Elem()
			}
			decodeStruct(vals, fv, tag, strict, fields, active)
			continue
		}

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

func structHasValue(vals url.Values, rt reflect.Type, tag string, strict bool, active map[reflect.Type]bool) bool {
	if active[rt] {
		return false
	}
	active[rt] = true
	defer delete(active, rt)

	for _, f := range structFields(rt, tag) {
		if f.embedded {
			if structHasValue(vals, indirectType(rt.Field(f.index).Type), tag, strict, active) {
				return true
			}
			continue
		}
		if strict && !f.tagged {
			continue
		}
		if _, _, ok := lookupValues(vals, f.keys); ok {
			return true
		}
	}
	return false
}

type fieldInfo struct {
	keys     []string
	layout   string
	index    int
	tagged   bool
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

func buildFields(rt reflect.Type, tag string) []fieldInfo {
	plan := make([]fieldInfo, 0, rt.NumField())
	for i := range rt.NumField() {
		ft := rt.Field(i)
		name, layout, tagged, skip := fieldName(ft, tag)
		if skip {
			continue
		}
		embedded := ft.Anonymous && !tagged && indirectType(ft.Type).Kind() == reflect.Struct

		if !ft.IsExported() && !embedded {
			continue
		}

		f := fieldInfo{layout: layout, index: i, tagged: tagged, embedded: embedded}
		if !embedded {
			f.keys = fieldKeys(name, ft.Name)
		}
		plan = append(plan, f)
	}
	return plan
}

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
		add(textproto.CanonicalMIMEHeaderKey(n))
	}
	return keys
}

func fieldName(ft reflect.StructField, tag string) (name, layout string, tagged, skip bool) {
	layout = ft.Tag.Get("format")
	for _, key := range []string{tag, "json"} {
		v, ok := ft.Tag.Lookup(key)
		if !ok {
			continue
		}
		name, opts, hasOpts := strings.Cut(v, ",")
		if name == "-" && !hasOpts {
			return "", layout, true, true
		}
		if name != "" {
			return name, layout, true, false
		}
		// encoding/json reads `json:",omitempty"` as "this field, under its Go
		// name, with options". The field is tagged; only the name is defaulted.
		// StrictBind drops untagged fields, so reading it as untagged silently
		// zeroed a field the author had annotated on purpose.
		if hasOpts && opts != "" {
			return ft.Name, layout, true, false
		}
	}
	return ft.Name, layout, false, false
}

func lookupValues(vals url.Values, keys []string) (string, []string, bool) {
	for _, k := range keys {
		if v, ok := vals[k]; ok {
			return k, v, true
		}
	}
	return "", nil, false
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
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return setScalar(fv.Elem(), s, layout)
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
