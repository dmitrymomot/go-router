package routertest

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// JSON reports a body whose JSON is not equal to want. Both sides are decoded
// before they are compared, so key order, white space and the spelling of a
// number do not count, and the failure lists every path where they differ.
//
// want is encoded with [json.Marshal]: pass a map, a slice or a struct, and
// pass a [jsontext.Value] for JSON text you already hold. A Go string is the
// JSON string, not JSON text.
func (e *Expect) JSON(want any) *Expect {
	e.tb.Helper()
	got, err := decodeJSON(e.res.Body)
	if err != nil {
		e.tb.Errorf("%sbody is not JSON: %v; body: %s", e.where(), err, e.res.Body)
		return e
	}
	wantV, err := normalizeJSON(want)
	if err != nil {
		e.tb.Errorf("%scannot encode the wanted JSON: %v", e.where(), err)
		return e
	}
	if diffs := diffJSON(nil, got, wantV, nil); len(diffs) > 0 {
		e.tb.Errorf("%sJSON differs:\n  %s\nbody: %s", e.where(), strings.Join(diffs, "\n  "), e.res.Body)
	}
	return e
}

// JSONPath reports a value of the JSON body, at path, that is not equal to
// want. A path is the keys and indexes from the root, joined with dots, as in
// items.0.name: a segment picks a key of an object and an index of an array.
// An empty path is the whole body. A key that holds a dot cannot be named.
//
// want is compared as [Expect.JSON] compares it.
func (e *Expect) JSONPath(path string, want any) *Expect {
	e.tb.Helper()
	root, err := decodeJSON(e.res.Body)
	if err != nil {
		e.tb.Errorf("%sbody is not JSON: %v; body: %s", e.where(), err, e.res.Body)
		return e
	}
	got, err := lookupJSON(root, path)
	if err != nil {
		e.tb.Errorf("%s%v; body: %s", e.where(), err, e.res.Body)
		return e
	}
	wantV, err := normalizeJSON(want)
	if err != nil {
		e.tb.Errorf("%scannot encode the wanted JSON: %v", e.where(), err)
		return e
	}
	var prefix []string
	if path != "" {
		prefix = strings.Split(path, ".")
	}
	if diffs := diffJSON(prefix, got, wantV, nil); len(diffs) > 0 {
		e.tb.Errorf("%sJSON at %q differs:\n  %s\nbody: %s", e.where(), path, strings.Join(diffs, "\n  "), e.res.Body)
	}
	return e
}

func decodeJSON(b []byte) (any, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// normalizeJSON turns a Go value into what decoding its JSON gives, so the
// two sides of a comparison hold the same kinds of value.
func normalizeJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeJSON(b)
}

func lookupJSON(v any, path string) (any, error) {
	if path == "" {
		return v, nil
	}
	walked := make([]string, 0, 4)
	for seg := range strings.SplitSeq(path, ".") {
		switch node := v.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, fmt.Errorf("JSON path %q: no key %q at %s", path, seg, pathName(walked))
			}
			v = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return nil, fmt.Errorf("JSON path %q: %q is not an index of the %d-element array at %s",
					path, seg, len(node), pathName(walked))
			}
			v = node[i]
		default:
			return nil, fmt.Errorf("JSON path %q: %s holds %s, which has no %q", path, pathName(walked), show(v), seg)
		}
		walked = append(walked, seg)
	}
	return v, nil
}

func pathName(path []string) string {
	if len(path) == 0 {
		return "the root"
	}
	return strings.Join(path, ".")
}

// diffJSON lists every place where got and want differ, each with its path.
func diffJSON(path []string, got, want any, out []string) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return append(out, mismatch(path, got, want))
		}
		for _, k := range slices.Sorted(maps.Keys(w)) {
			p := append(slices.Clone(path), k)
			gv, present := g[k]
			if !present {
				out = append(out, pathName(p)+": missing, want "+show(w[k]))
				continue
			}
			out = diffJSON(p, gv, w[k], out)
		}
		for _, k := range slices.Sorted(maps.Keys(g)) {
			if _, wanted := w[k]; !wanted {
				out = append(out, pathName(append(slices.Clone(path), k))+": unexpected, got "+show(g[k]))
			}
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok {
			return append(out, mismatch(path, got, want))
		}
		for i := range max(len(g), len(w)) {
			p := append(slices.Clone(path), strconv.Itoa(i))
			switch {
			case i >= len(g):
				out = append(out, pathName(p)+": missing, want "+show(w[i]))
			case i >= len(w):
				out = append(out, pathName(p)+": unexpected, got "+show(g[i]))
			default:
				out = diffJSON(p, g[i], w[i], out)
			}
		}
		return out
	default:
		if !scalarEqual(got, want) {
			return append(out, mismatch(path, got, want))
		}
		return out
	}
}

func scalarEqual(a, b any) bool {
	switch a.(type) {
	case map[string]any, []any:
		return false
	}
	return a == b
}

func mismatch(path []string, got, want any) string {
	return pathName(path) + ": got " + show(got) + ", want " + show(want)
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
