package router

import "testing"

func TestFillPatternWritesEveryPartAndKeepsAnUnbalancedBrace(t *testing.T) {
	value := func(name, constraint string) string { return "<" + name + ":" + constraint + ">" }
	tests := []struct {
		pattern string
		host    bool
		want    string
	}{
		{pattern: "/a/{id:int}/{rest...}", want: "/a/%3Cid:int%3E/%3Crest:%3E"},
		{pattern: "/r/rep-{d:[0-9]{4}}.csv", want: "/r/rep-%3Cd:%5B0-9%5D%7B4%7D%3E.csv"},
		{pattern: "/files/{*...}", want: "/files/%3C%2A:%3E"},
		{pattern: "/a/{id", want: "/a/{id"},
		{pattern: "/a/x{id}{rest", want: "/a/x%3Cid:%3E{rest"},
		{pattern: "{t.example.test", host: true, want: "{t.example.test"},
		{pattern: "*.{t:slug}.test", host: true, want: "<*:>.<t:slug>.test"},
	}
	for _, tt := range tests {
		if got, _ := fillPattern(tt.pattern, tt.host, value); got != tt.want {
			t.Errorf("fillPattern(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}
