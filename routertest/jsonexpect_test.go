package routertest_test

import (
	"encoding/json/jsontext"
	"net/http"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

func jsonRouter(body string) *router.Router[*appContext] {
	r := router.New(newContext)
	r.GET("/", func(c *appContext) error {
		c.SetHeader(router.HeaderContentType, router.MIMEApplicationJSON)
		return c.String(http.StatusOK, body)
	})
	return r
}

const catalog = `{
  "total": 2,
  "items": [{"name": "pen", "price": 1.50}, {"name": "ink", "price": 4}],
  "owner": {"name": "ann", "tags": []},
  "note": null
}`

func TestExpectJSONIgnoresOrderSpacingAndNumberSpelling(t *testing.T) {
	res := routertest.Get(jsonRouter(catalog), "/")

	tb := new(recordingTB)
	res.Expect(tb).JSON(map[string]any{
		"note":  nil,
		"owner": map[string]any{"tags": []string{}, "name": "ann"},
		"items": []map[string]any{{"price": 1.5, "name": "pen"}, {"name": "ink", "price": 4}},
		"total": 2,
	})
	if tb.failed {
		t.Errorf("an equal JSON failed: %v", tb.errors)
	}

	tb = new(recordingTB)
	res.Expect(tb).JSON(jsontext.Value(`{"total":2,"items":[{"name":"pen","price":1.5},{"name":"ink","price":4.0}],"owner":{"name":"ann","tags":[]},"note":null}`))
	if tb.failed {
		t.Errorf("an equal JSON text failed: %v", tb.errors)
	}
}

func TestExpectJSONListsEveryDifference(t *testing.T) {
	res := routertest.Get(jsonRouter(catalog), "/")

	tb := new(recordingTB)
	res.Expect(tb).JSON(map[string]any{
		"total": 3,
		"items": []map[string]any{{"name": "pen", "price": 1.5}},
		"owner": map[string]any{"name": "bob", "tags": []string{}, "age": 7},
		"extra": true,
	})
	if len(tb.errors) != 1 {
		t.Fatalf("errors = %q, want one", tb.errors)
	}
	for _, want := range []string{
		"total: got 2, want 3",
		"items.1: unexpected",
		"owner.name: got \"ann\", want \"bob\"",
		"owner.age: missing, want 7",
		"extra: missing, want true",
		"note: unexpected, got null",
		"body: {",
	} {
		if !strings.Contains(tb.errors[0], want) {
			t.Errorf("the message lacks %q:\n%s", want, tb.errors[0])
		}
	}
}

func TestExpectJSONReportsATypeMismatchAndABodyThatIsNotJSON(t *testing.T) {
	tb := new(recordingTB)
	routertest.Get(jsonRouter(`{"a":[1]}`), "/").Expect(tb).JSON(map[string]any{"a": map[string]any{"b": 1}})
	if len(tb.errors) != 1 || !strings.Contains(tb.errors[0], `a: got [1], want {"b":1}`) {
		t.Errorf("errors = %q, want a type mismatch at a", tb.errors)
	}

	tb = new(recordingTB)
	routertest.Get(jsonRouter(`not json`), "/").Expect(tb).JSON(1)
	if len(tb.errors) != 1 || !strings.Contains(tb.errors[0], "body is not JSON") {
		t.Errorf("errors = %q, want a body that is not JSON", tb.errors)
	}

	tb = new(recordingTB)
	routertest.Get(jsonRouter(`1`), "/").Expect(tb).JSON(func() {})
	if len(tb.errors) != 1 || !strings.Contains(tb.errors[0], "cannot encode") {
		t.Errorf("errors = %q, want an encoding failure", tb.errors)
	}
}

func TestExpectJSONPath(t *testing.T) {
	res := routertest.Get(jsonRouter(catalog), "/")

	for _, tc := range []struct {
		path string
		want any
		ok   bool
		msg  string
	}{
		{"total", 2, true, ""},
		{"items.0.name", "pen", true, ""},
		{"items.1.price", 4, true, ""},
		{"owner", map[string]any{"name": "ann", "tags": []string{}}, true, ""},
		{"note", nil, true, ""},
		{"", map[string]any{"total": 2}, false, "total"},
		{"items.1.name", "pen", false, `items.1.name: got "ink", want "pen"`},
		{"items.2", "x", false, "is not an index of the 2-element array at items"},
		{"items.first", "x", false, `"first" is not an index`},
		{"items.-1", "x", false, `"-1" is not an index`},
		{"owner.age", 1, false, `no key "age" at owner`},
		{"total.x", 1, false, `holds 2, which has no "x"`},
	} {
		tb := new(recordingTB)
		res.Expect(tb).JSONPath(tc.path, tc.want)
		switch {
		case tc.ok && tb.failed:
			t.Errorf("JSONPath(%q) failed: %v", tc.path, tb.errors)
		case !tc.ok && (len(tb.errors) != 1 || !strings.Contains(tb.errors[0], tc.msg)):
			t.Errorf("JSONPath(%q) errors = %q, want one with %q", tc.path, tb.errors, tc.msg)
		}
	}
}

func TestExpectJSONChains(t *testing.T) {
	routertest.Get(jsonRouter(`{"a":{"b":[1,2]}}`), "/").Expect(t).
		Status(http.StatusOK).
		ContentType(router.MIMEApplicationJSON).
		JSONPath("a.b.1", 2).
		JSON(map[string]any{"a": map[string]any{"b": []int{1, 2}}})
}
