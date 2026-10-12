package router

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeValuesSplitsASlice(t *testing.T) {
	type in struct {
		IDs  []int    `query:"ids,split"`
		Tags []string `query:"tag,split"`
		Raw  []string `query:"raw"`
	}
	tests := []struct {
		name string
		vals url.Values
		want in
	}{
		{"one value", url.Values{"ids": {"1,2,3"}}, in{IDs: []int{1, 2, 3}}},
		{"repeated values", url.Values{"ids": {"1,2", "3"}}, in{IDs: []int{1, 2, 3}}},
		{"spaces and empty parts", url.Values{"tag": {" a , ,b,"}}, in{Tags: []string{"a", "b"}}},
		{"no split option", url.Values{"raw": {"a,b"}}, in{Raw: []string{"a,b"}}},
		{"an empty value", url.Values{"ids": {""}}, in{IDs: []int{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got in
			if fields := decode(tt.vals, &got, "query"); fields != nil {
				t.Fatalf("fields = %v", fields)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}

	var got in
	fields := decode(url.Values{"ids": {"1,x"}}, &got, "query")
	if want := []FieldError{{Field: "ids", Message: "is not a valid integer"}}; !reflect.DeepEqual(fields, want) {
		t.Errorf("fields = %v, want %v", fields, want)
	}
}

func TestBindHeaderSplitsAList(t *testing.T) {
	type in struct {
		Langs []string `header:"Accept-Language,split"`
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Accept-Language", "en, de")
	req.Header.Add("Accept-Language", "fr")
	got, err := NewBase(httptest.NewRecorder(), req).BindHeader[in]()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"en", "de", "fr"}; !reflect.DeepEqual(got.Langs, want) {
		t.Errorf("Langs = %q, want %q", got.Langs, want)
	}
}

func TestDecodeValuesFillsADefault(t *testing.T) {
	type in struct {
		Page  int      `query:"page" default:"1"`
		Sort  string   `query:"sort" default:"name"`
		Tags  []string `query:"tag" default:"a,b"`
		Limit *int     `query:"limit" default:"20"`
	}
	ptr := func(n int) *int { return &n }
	tests := []struct {
		name string
		vals url.Values
		want in
	}{
		{"no values", nil, in{Page: 1, Sort: "name", Tags: []string{"a", "b"}, Limit: ptr(20)}},
		{"every value", url.Values{"page": {"3"}, "sort": {"id"}, "tag": {"x"}, "limit": {"5"}},
			in{Page: 3, Sort: "id", Tags: []string{"x"}, Limit: ptr(5)}},
		{"an empty value is not absent", url.Values{"page": {""}, "sort": {""}},
			in{Page: 0, Sort: "", Tags: []string{"a", "b"}, Limit: ptr(20)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got in
			if fields := decode(tt.vals, &got, "query"); fields != nil {
				t.Fatalf("fields = %v", fields)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}

	// Each value gets a slice of its own, so a handler that changes one does
	// not change the next.
	var a, b in
	decode(nil, &a, "query")
	a.Tags[0] = "changed"
	decode(nil, &b, "query")
	if b.Tags[0] != "a" {
		t.Errorf("the default slice is shared: %q", b.Tags)
	}
}

// A default fills a field that no source set, so it never overwrites the body.
func TestBindKeepsTheBodyOverADefault(t *testing.T) {
	type in struct {
		Limit int `json:"limit" query:"limit" default:"10"`
		Page  int `query:"page" default:"1"`
	}
	r := newTestRouter()
	r.POST("/in", func(c *tctx) error {
		in, err := c.Bind[in]()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, in)
	})
	rec := post(r, "/in", MIMEApplicationJSON, `{"limit":5}`)
	if got, want := strings.TrimSpace(rec.Body.String()), `{"limit":5,"Page":1}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	rec = post(r, "/in", MIMEApplicationJSON, `{}`)
	if got, want := strings.TrimSpace(rec.Body.String()), `{"limit":10,"Page":1}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestDecodeValuesFillsANestedStruct(t *testing.T) {
	type Geo struct {
		Lat float64 `form:"lat"`
	}
	type Addr struct {
		City string `form:"city"`
		Zip  string `form:"zip" default:"00000"`
		Geo  *Geo   `form:"geo"`
	}
	type in struct {
		Name string `form:"name"`
		Home Addr   `form:"home"`
		Work *Addr  `form:"work"`
	}

	var got in
	fields := decode(url.Values{"name": {"ann"}, "home.city": {"Kyiv"}, "home.geo.lat": {"50.4"}}, &got, "form")
	if fields != nil {
		t.Fatalf("fields = %v", fields)
	}
	want := in{Name: "ann", Home: Addr{City: "Kyiv", Zip: "00000", Geo: &Geo{Lat: 50.4}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	got = in{}
	decode(url.Values{"work.city": {"Lviv"}}, &got, "form")
	if got.Work == nil || got.Work.City != "Lviv" || got.Work.Zip != "00000" || got.Work.Geo != nil {
		t.Errorf("Work = %+v, want Lviv with the default zip and no Geo", got.Work)
	}

	got = in{}
	fields = decode(url.Values{"home.geo.lat": {"north"}}, &got, "form")
	if want := []FieldError{{Field: "home.geo.lat", Message: "is not a valid number"}}; !reflect.DeepEqual(fields, want) {
		t.Errorf("fields = %v, want %v", fields, want)
	}
}

type chainNode struct {
	Name string     `query:"name"`
	Next *chainNode `query:"next"`
}

func TestDecodeValuesStopsARecursiveStructWithTheKeys(t *testing.T) {
	var got chainNode
	if fields := decode(url.Values{"name": {"a"}, "next.next.name": {"c"}}, &got, "query"); fields != nil {
		t.Fatalf("fields = %v", fields)
	}
	if got.Name != "a" || got.Next == nil || got.Next.Next == nil || got.Next.Next.Name != "c" || got.Next.Next.Next != nil {
		t.Errorf("got %+v", got)
	}
}

type labelKey string

func TestDecodeValuesFillsAMap(t *testing.T) {
	type in struct {
		Price  map[string]int      `query:"price"`
		Labels map[labelKey]string `query:"label"`
		Lists  map[string][]int    `query:"list,split"`
		Kept   map[string]string   `query:"kept"`
		Nested struct {
			M map[string]bool `query:"m"`
		} `query:"n"`
	}
	vals := url.Values{
		"price[a]": {"1"}, "price[b]": {"2"}, "price": {"9"}, "price[c][d]": {"3"}, "price[]": {"4"},
		"label[env]": {"prod"},
		"list[x]":    {"1,2", "3"},
		"n.m[on]":    {"on"},
	}
	var got in
	if fields := decode(vals, &got, "query"); fields != nil {
		t.Fatalf("fields = %v", fields)
	}
	want := in{
		Price:  map[string]int{"a": 1, "b": 2, "": 4},
		Labels: map[labelKey]string{"env": "prod"},
		Lists:  map[string][]int{"x": {1, 2, 3}},
	}
	want.Nested.M = map[string]bool{"on": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	got = in{}
	fields := decode(url.Values{"price[b]": {"x"}, "price[a]": {"y"}, "price[c]": {"3"}}, &got, "query")
	want2 := []FieldError{{Field: "price[a]", Message: "is not a valid integer"}, {Field: "price[b]", Message: "is not a valid integer"}}
	if !reflect.DeepEqual(fields, want2) {
		t.Errorf("fields = %v, want %v", fields, want2)
	}
	if !reflect.DeepEqual(got.Price, map[string]int{"c": 3}) {
		t.Errorf("Price = %v, want the key that parsed", got.Price)
	}
}

// A field that the source cannot fill is a bug in the struct, so every Bind
// of the type answers 500, with a cause that names the field, whether or not
// the request carries a value for it.
func TestBindAnswers500ForAFieldItCannotFill(t *testing.T) {
	type badChan struct {
		C chan int `query:"c"`
	}
	type badMapKey struct {
		M map[int]string `query:"m"`
	}
	type badFunc struct {
		F func() `form:"f"`
	}
	type badSlice struct {
		S []struct{ N int } `query:"s"`
	}
	type badHeader struct {
		H struct{ N int } `header:"X-H"`
	}
	type badSplit struct {
		N int `query:"n,split"`
	}
	type badDefault struct {
		N int `query:"n" default:"many"`
	}
	type badDefaultKind struct {
		A struct {
			N int `query:"n"`
		} `query:"a" default:"x"`
	}
	type Inner struct {
		C chan int `query:"c"`
	}
	type badDeep struct {
		Inner
	}
	type badNested struct {
		In *struct {
			F func() `query:"f"`
		} `query:"in"`
	}

	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	var bindErr error
	route := func(path string, bind func(*tctx) error) {
		r.GET(path, func(c *tctx) error { bindErr = bind(c); return bindErr })
		r.POST(path, func(c *tctx) error { bindErr = bind(c); return bindErr })
	}
	route("/chan", func(c *tctx) error { _, err := c.BindQuery[badChan](); return err })
	route("/mapkey", func(c *tctx) error { _, err := c.Bind[badMapKey](); return err })
	route("/func", func(c *tctx) error { _, err := c.BindForm[badFunc](); return err })
	route("/slice", func(c *tctx) error { _, err := c.BindQuery[badSlice](); return err })
	route("/header", func(c *tctx) error { _, err := c.BindHeader[badHeader](); return err })
	route("/split", func(c *tctx) error { _, err := c.BindQuery[badSplit](); return err })
	route("/default", func(c *tctx) error { _, err := c.BindQuery[badDefault](); return err })
	route("/defaultkind", func(c *tctx) error { _, err := c.BindQuery[badDefaultKind](); return err })
	route("/deep", func(c *tctx) error { _, err := c.Bind[badDeep](); return err })
	route("/nested", func(c *tctx) error { _, err := c.BindQuery[*badNested](); return err })

	for path, field := range map[string]string{
		"/chan": "field C", "/mapkey": "field M", "/func": "field F", "/slice": "field S",
		"/header": "field H", "/split": "field N", "/default": "field N", "/defaultkind": "field A",
		"/deep": "field C", "/nested": "field F",
	} {
		method := http.MethodGet
		if path == "/func" {
			method = http.MethodPost
		}
		rec := doBody(r, method, path, MIMEApplicationForm, "")
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", path, rec.Code)
			continue
		}
		if he, ok := errors.AsType[*HTTPError](bindErr); !ok || he.Err == nil || !strings.Contains(he.Err.Error(), field) {
			t.Errorf("%s: cause = %v, want it to name %s", path, bindErr, field)
		}
	}
}

// BindPath fills a struct from the route parameters without a map of them,
// and takes a default for a parameter that the route lacks.
func TestBindPathTakesADefault(t *testing.T) {
	type in struct {
		ID   int    `param:"id"`
		Kind string `param:"kind" default:"all"`
	}
	r := newTestRouter()
	r.GET("/items/{id}", func(c *tctx) error {
		in, err := c.BindPath[in]()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, in)
	})
	if got, want := strings.TrimSpace(do(r, http.MethodGet, "/items/7").Body.String()), `{"ID":7,"Kind":"all"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}
