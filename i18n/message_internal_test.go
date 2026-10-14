package i18n

import (
	"testing"
)

func TestCompileAndRender(t *testing.T) {
	tests := []struct {
		name  string
		msg   string
		args  []any
		count int
		hasN  bool
		want  string
	}{
		{"literal", "Hello world", nil, 0, false, "Hello world"},
		{"one placeholder", "Hello, {{name}}!", []any{"name", "Juan"}, 0, false, "Hello, Juan!"},
		{"two placeholders", "{{a}} and {{b}}", []any{"a", "x", "b", "y"}, 0, false, "x and y"},
		{"missing arg keeps placeholder", "Hi {{name}}", nil, 0, false, "Hi {{name}}"},
		{"count injected", "{{count}} items", nil, 5, true, "5 items"},
		{"explicit count wins", "{{count}} items", []any{"count", "five"}, 5, true, "five items"},
		{"count not injected without hasCount", "{{count}} items", nil, 5, false, "{{count}} items"},
		{"int arg", "n={{n}}", []any{"n", 42}, 0, false, "n=42"},
		{"int64 arg", "n={{n}}", []any{"n", int64(-7)}, 0, false, "n=-7"},
		{"float arg", "f={{f}}", []any{"f", 1.5}, 0, false, "f=1.5"},
		{"stringer arg", "d={{d}}", []any{"d", PluralCategory(3)}, 0, false, "d=few"},
		{"fallback arg", "b={{b}}", []any{"b", true}, 0, false, "b=true"},
		{"unmatched open brace literal", "a {{b c", nil, 0, false, "a {{b c"},
		{"unclosed placeholder", "a {{b", nil, 0, false, "a {{b"},
		{"empty name literal", "a {{}} b", nil, 0, false, "a {{}} b"},
		{"spaced name literal", "a {{b c}} d", nil, 0, false, "a {{b c}} d"},
		{"nested braces literal", "a {{{b}} d", nil, 0, false, "a {{{b}} d"},
		{"adjacent", "{{a}}{{b}}", []any{"a", "1", "b", "2"}, 0, false, "12"},
		{"odd args ignore dangling", "x {{a}}", []any{"a", "ok", "dangling"}, 0, false, "x ok"},
		{"non-string key ignored", "x {{a}}", []any{1, "nope", "a", "ok"}, 0, false, "x ok"},
		{"unicode", "Привіт, {{name}}!", []any{"name", "Олю"}, 0, false, "Привіт, Олю!"},
		{"empty message", "", nil, 0, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := compileMessage(tc.msg)
			if got := m.render(tc.args, tc.count, tc.hasN); got != tc.want {
				t.Errorf("render(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

func TestCompileSegments(t *testing.T) {
	m := compileMessage("Hello, {{name}}! Bye")
	if len(m.segs) != 3 {
		t.Fatalf("got %d segments, want 3: %+v", len(m.segs), m.segs)
	}
	if m.segs[0].lit != "Hello, " || m.segs[1].arg != "name" || m.segs[2].lit != "! Bye" {
		t.Errorf("segments = %+v", m.segs)
	}
}

func TestRenderLiteralFastPath(t *testing.T) {
	// A literal-only message must not copy: render returns the interned literal.
	m := compileMessage("static")
	if allocs := testing.AllocsPerRun(100, func() { _ = m.render(nil, 0, false) }); allocs != 0 {
		t.Errorf("literal-only render allocated %v times, want 0", allocs)
	}
}

func TestAppendToDoesNotClobber(t *testing.T) {
	m := compileMessage("{{a}}")
	got := m.appendTo([]byte("prefix:"), []any{"a", "v"}, 0, false)
	if string(got) != "prefix:v" {
		t.Errorf("appendTo = %q, want %q", got, "prefix:v")
	}
}

func FuzzCompileMessage(f *testing.F) {
	f.Add("Hello, {{name}}!")
	f.Add("{{a}}{{b}}{{")
	f.Add("}}{{}}{{x y}}")
	f.Add("{{count}}")
	f.Fuzz(func(t *testing.T, s string) {
		// Must never panic. With no args and hasCount=false, every
		// placeholder segment (matched or not) falls back to rendering its
		// exact original "{{name}}" bytes, so the round trip holds for any
		// input, not just placeholder-free ones.
		if got := compileMessage(s).render(nil, 0, false); got != s {
			t.Errorf("render of %q = %q", s, got)
		}
	})
}
