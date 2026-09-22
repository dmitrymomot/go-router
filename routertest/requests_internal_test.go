package routertest

import "testing"

func TestFillPatternKeepsAnUnbalancedBrace(t *testing.T) {
	value := func(name, _ string) string { return "<" + name + ">" }
	tests := []struct {
		pattern string
		host    bool
		want    string
	}{
		{pattern: "/a/{id", want: "/a/{id"},
		{pattern: "/a/x{id}{rest", want: "/a/x%3Cid%3E{rest"},
		{pattern: "{t.example.test", host: true, want: "{t.example.test"},
		{pattern: "*.{t}.test", host: true, want: "x.<t>.test"},
	}
	for _, tt := range tests {
		if got, _ := fillPattern(tt.pattern, tt.host, value); got != tt.want {
			t.Errorf("fillPattern(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}
