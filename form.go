package router

import (
	"encoding"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// decodeValues fills dst, which must be a non-nil pointer to a struct, from a
// set of URL values. It reads the field name from the named tag, then from the
// json tag, and falls back to the field name itself and its lower-case form.
//
// It handles strings, booleans, the integer and float kinds, [time.Duration],
// any type that implements [encoding.TextUnmarshaler] such as [time.Time],
// pointers to those, slices of those, and embedded structs.
func decodeValues(vals url.Values, dst any, tag string) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("decode target must be a non-nil pointer, got %T", dst)
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("decode target must point at a struct, got %s", rv.Kind())
	}
	return decodeStruct(vals, rv, tag)
}

func decodeStruct(vals url.Values, rv reflect.Value, tag string) error {
	rt := rv.Type()
	for i := range rt.NumField() {
		ft := rt.Field(i)
		embeddedStruct := ft.Anonymous && indirectType(ft.Type).Kind() == reflect.Struct

		// An embedded struct promotes its exported fields even when its own
		// type name is unexported, and reflect can set those fields.
		if !ft.IsExported() && !embeddedStruct {
			continue
		}
		name, tagged, skip := fieldName(ft, tag)
		if skip {
			continue
		}

		fv := rv.Field(i)
		if embeddedStruct && !tagged {
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					if !fv.CanSet() {
						continue
					}
					fv.Set(reflect.New(ft.Type.Elem()))
				}
				fv = fv.Elem()
			}
			if err := decodeStruct(vals, fv, tag); err != nil {
				return err
			}
			continue
		}

		raw, ok := lookupValues(vals, name, ft.Name)
		if !ok {
			continue
		}
		if err := setField(fv, raw); err != nil {
			return fmt.Errorf("field %s: %w", ft.Name, err)
		}
	}
	return nil
}

// fieldName returns the key that a field reads. tagged reports whether a tag
// named the field, and skip reports a "-" tag.
func fieldName(ft reflect.StructField, tag string) (name string, tagged, skip bool) {
	for _, key := range []string{tag, "json"} {
		v, ok := ft.Tag.Lookup(key)
		if !ok {
			continue
		}
		v, _, _ = strings.Cut(v, ",")
		if v == "-" {
			return "", true, true
		}
		if v != "" {
			return v, true, false
		}
	}
	return ft.Name, false, false
}

// lookupValues finds a key, first as written and then in lower case.
func lookupValues(vals url.Values, names ...string) ([]string, bool) {
	for _, n := range names {
		if v, ok := vals[n]; ok {
			return v, true
		}
		if lower := strings.ToLower(n); lower != n {
			if v, ok := vals[lower]; ok {
				return v, true
			}
		}
	}
	return nil, false
}

func indirectType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

// setField assigns one or more raw values to a field.
func setField(fv reflect.Value, raw []string) error {
	if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() != reflect.Uint8 {
		out := reflect.MakeSlice(fv.Type(), len(raw), len(raw))
		for i, s := range raw {
			if err := setScalar(out.Index(i), s); err != nil {
				return err
			}
		}
		fv.Set(out)
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	return setScalar(fv, raw[0])
}

// setScalar parses s into a single value.
func setScalar(fv reflect.Value, s string) error {
	if fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return setScalar(fv.Elem(), s)
	}

	// An empty value leaves a non-string field at its zero value, so that
	// "?limit=" does not fail.
	if s == "" && fv.Kind() != reflect.String {
		return nil
	}

	if fv.CanAddr() {
		if u, ok := reflect.TypeAssert[encoding.TextUnmarshaler](fv.Addr()); ok {
			if err := u.UnmarshalText([]byte(s)); err != nil {
				return fmt.Errorf("cannot parse %q as %s: %w", s, fv.Type(), err)
			}
			return nil
		}
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
