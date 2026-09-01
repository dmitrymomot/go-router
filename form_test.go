package router

import (
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestSetScalarParsesEveryKind(t *testing.T) {
	type target struct {
		Str      string
		Bool     bool
		Int      int
		Int8     int8
		Uint     uint
		Float    float64
		Duration time.Duration
		Time     time.Time
	}

	tests := []struct {
		name  string
		field string
		in    string
		want  any
		bad   bool
	}{
		{name: "a string", field: "Str", in: "go", want: "go"},
		{name: "an empty string", field: "Str", in: "", want: ""},
		{name: "a boolean", field: "Bool", in: "true", want: true},
		{name: "a boolean that does not parse", field: "Bool", in: "yes", bad: true},
		{name: "an integer", field: "Int", in: "-7", want: -7},
		{name: "an integer that does not parse", field: "Int", in: "abc", bad: true},
		{name: "an integer that does not fit", field: "Int8", in: "300", bad: true},
		{name: "an unsigned integer", field: "Uint", in: "7", want: uint(7)},
		{name: "an unsigned integer of a negative", field: "Uint", in: "-7", bad: true},
		{name: "a float", field: "Float", in: "1.5", want: 1.5},
		{name: "a duration", field: "Duration", in: "90s", want: 90 * time.Second},
		{name: "a duration that does not parse", field: "Duration", in: "soon", bad: true},
		{name: "a time", field: "Time", in: "2026-01-02T03:04:05Z", want: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		{name: "a time that does not parse", field: "Time", in: "2026-01-02", bad: true},
		{name: "an empty integer", field: "Int", in: "", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst target
			fv := reflect.ValueOf(&dst).Elem().FieldByName(tt.field)
			err := setScalar(fv, tt.in, "")
			if tt.bad {
				if err == nil {
					t.Fatalf("setScalar(%q) into %s = nil, want an error", tt.in, tt.field)
				}
				return
			}
			if err != nil {
				t.Fatalf("setScalar(%q): %v", tt.in, err)
			}
			if got := fv.Interface(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("field = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSetScalarFillsAPointer(t *testing.T) {
	var dst struct{ Page *int }
	fv := reflect.ValueOf(&dst).Elem().Field(0)
	if err := setScalar(fv, "7", ""); err != nil {
		t.Fatalf("setScalar: %v", err)
	}
	if dst.Page == nil || *dst.Page != 7 {
		t.Errorf("Page = %v", dst.Page)
	}
}

func TestSetScalarLeavesAPointerNilForAnEmptyValue(t *testing.T) {
	var dst struct {
		Page *int
		Name *string
	}
	rv := reflect.ValueOf(&dst).Elem()

	if err := setScalar(rv.Field(0), "", ""); err != nil {
		t.Fatalf("setScalar: %v", err)
	}
	if dst.Page != nil {
		t.Errorf("Page = %d, want nil for an empty value", *dst.Page)
	}

	if err := setScalar(rv.Field(1), "", ""); err != nil {
		t.Fatalf("setScalar: %v", err)
	}
	if dst.Name == nil || *dst.Name != "" {
		t.Errorf("Name = %v, want a pointer to an empty string", dst.Name)
	}
}

func TestSetScalarFillsAByteSlice(t *testing.T) {
	var dst struct {
		Data  []byte
		Extra *[]byte
	}
	rv := reflect.ValueOf(&dst).Elem()
	if err := setScalar(rv.Field(0), "abc", ""); err != nil {
		t.Fatalf("setScalar: %v", err)
	}
	if string(dst.Data) != "abc" {
		t.Errorf("Data = %q, want %q", dst.Data, "abc")
	}
	if err := setScalar(rv.Field(1), "xyz", ""); err != nil {
		t.Fatalf("setScalar: %v", err)
	}
	if dst.Extra == nil || string(*dst.Extra) != "xyz" {
		t.Errorf("Extra = %v, want a pointer to %q", dst.Extra, "xyz")
	}
}

func TestDecodeValuesFillsAByteSlice(t *testing.T) {
	var got struct {
		Data []byte `query:"data"`
	}
	fields, err := decodeValues(url.Values{"data": {"abc"}}, &got, "query", false)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(fields) != 0 {
		t.Fatalf("field errors = %v, want none", fields)
	}
	if string(got.Data) != "abc" {
		t.Errorf("Data = %q, want %q", got.Data, "abc")
	}
}

func TestSetFieldFillsASlice(t *testing.T) {
	var dst struct{ Tags []int }
	fv := reflect.ValueOf(&dst).Elem().Field(0)
	if err := setField(fv, []string{"1", "2", "3"}, ""); err != nil {
		t.Fatalf("setField: %v", err)
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(dst.Tags, want) {
		t.Errorf("Tags = %v, want %v", dst.Tags, want)
	}
	if err := setField(fv, []string{"1", "x"}, ""); err == nil {
		t.Error("setField accepted an element that does not parse")
	}
}

func TestDecodeValuesReadsAFormatTag(t *testing.T) {
	tests := []struct {
		name   string
		layout string
		in     string
		want   time.Time
		bad    bool
	}{
		{
			name:   "a date, which is what an input type=date posts",
			layout: "2006-01-02",
			in:     "2026-01-02",
			want:   time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			name:   "a date and a time, which is what an input type=datetime-local posts",
			layout: "2006-01-02T15:04",
			in:     "2026-01-02T03:04",
			want:   time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
		},
		{name: "unix seconds", layout: "unix", in: "1767322800", want: time.Unix(1767322800, 0)},
		{name: "unix milliseconds", layout: "unixmilli", in: "1767322800123", want: time.UnixMilli(1767322800123)},
		{name: "unix nanoseconds", layout: "unixnano", in: "1767322800000000123", want: time.Unix(0, 1767322800000000123)},
		{name: "a date that does not fit the layout", layout: "2006-01-02", in: "02/01/2026", bad: true},
		{name: "a timestamp that is not a number", layout: "unix", in: "yesterday", bad: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := reflect.StructOf([]reflect.StructField{{
				Name: "Since",
				Type: reflect.TypeFor[time.Time](),
				Tag:  reflect.StructTag(`query:"since" format:"` + tt.layout + `"`),
			}})
			dst := reflect.New(rt)

			fields, err := decodeValues(url.Values{"since": {tt.in}}, dst.Interface(), "query", false)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if tt.bad {
				if len(fields) != 1 || fields[0].Field != "since" {
					t.Fatalf("fields = %+v, want one for since", fields)
				}
				return
			}
			if len(fields) != 0 {
				t.Fatalf("fields = %+v, want none", fields)
			}
			if got := dst.Elem().Field(0).Interface().(time.Time); !got.Equal(tt.want) {
				t.Errorf("Since = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeValuesKeepsRFC3339WithoutAFormatTag(t *testing.T) {
	var got struct {
		Since time.Time `query:"since"`
	}
	fields, err := decodeValues(url.Values{"since": {"2026-01-02T03:04:05Z"}}, &got, "query", false)
	if err != nil || len(fields) != 0 {
		t.Fatalf("decode: %v, %+v", err, fields)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !got.Since.Equal(want) {
		t.Errorf("Since = %v, want %v", got.Since, want)
	}

	fields, err = decodeValues(url.Values{"since": {"2026-01-02"}}, &got, "query", false)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(fields) != 1 {
		t.Errorf("fields = %+v, want one", fields)
	}
}

func TestDecodeValuesReadsAFormatTagForASliceAndAPointer(t *testing.T) {
	var got struct {
		Days []time.Time `query:"day" format:"2006-01-02"`
		Cut  *time.Time  `query:"cut" format:"2006-01-02"`
	}
	fields, err := decodeValues(url.Values{
		"day": {"2026-01-02", "2026-01-03"},
		"cut": {"2026-02-01"},
	}, &got, "query", false)
	if err != nil || len(fields) != 0 {
		t.Fatalf("decode: %v, %+v", err, fields)
	}
	if len(got.Days) != 2 || got.Days[1].Day() != 3 {
		t.Errorf("Days = %v", got.Days)
	}
	if got.Cut == nil || got.Cut.Month() != time.February {
		t.Errorf("Cut = %v", got.Cut)
	}
}

func TestDecodeValuesCollectsEveryFieldError(t *testing.T) {
	var got struct {
		Page  int           `query:"page"`
		Limit int           `query:"limit"`
		TTL   time.Duration `query:"ttl"`
		Term  string        `query:"q"`
	}
	fields, err := decodeValues(url.Values{
		"page":  {"a"},
		"limit": {"b"},
		"ttl":   {"c"},
		"q":     {"go"},
	}, &got, "query", false)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(fields) != 3 {
		t.Fatalf("fields = %+v, want three", fields)
	}
	for i, want := range []string{"page", "limit", "ttl"} {
		if fields[i].Field != want {
			t.Errorf("fields[%d].Field = %q, want %q", i, fields[i].Field, want)
		}
	}
	if got.Term != "go" {
		t.Errorf("Term = %q, want the decoder to have kept going", got.Term)
	}
}

func TestDecodeValuesNamesTheFieldAsTheRequestSpellsIt(t *testing.T) {
	var got struct {
		Page int
	}
	fields, err := decodeValues(url.Values{"page": {"a"}}, &got, "query", false)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(fields) != 1 || fields[0].Field != "page" {
		t.Fatalf("fields = %+v, want the lower-case key that matched", fields)
	}
}

func TestDecodeValuesFillsOnlyTaggedFieldsWhenStrict(t *testing.T) {
	type user struct {
		Name    string `form:"name"`
		IsAdmin bool
	}

	var loose user
	if _, err := decodeValues(url.Values{"name": {"bo"}, "isadmin": {"true"}}, &loose, "form", false); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !loose.IsAdmin {
		t.Error("the default mode no longer fills an untagged field, which the strict mode exists to stop")
	}

	var strict user
	if _, err := decodeValues(url.Values{"name": {"bo"}, "isadmin": {"true"}}, &strict, "form", true); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strict.Name != "bo" {
		t.Errorf("Name = %q, want the tagged field to be filled", strict.Name)
	}
	if strict.IsAdmin {
		t.Error("IsAdmin = true, want the untagged field to be left alone")
	}
}

func TestDecodeValuesRejectsABadTarget(t *testing.T) {
	tests := []struct {
		name string
		dst  any
	}{
		{name: "a value", dst: struct{}{}},
		{name: "a nil pointer", dst: (*struct{})(nil)},
		{name: "a pointer to a string", dst: new(string)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeValues(nil, tt.dst, "query", false); err == nil {
				t.Errorf("decodeValues(%T) = nil, want an error", tt.dst)
			}
		})
	}
}

func TestDecodeValuesSkipsADashTag(t *testing.T) {
	var got struct {
		Ignored string `query:"-"`
		Kept    string `query:"kept"`
	}
	if _, err := decodeValues(url.Values{"Ignored": {"x"}, "kept": {"y"}}, &got, "query", false); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ignored != "" || got.Kept != "y" {
		t.Errorf("got %+v", got)
	}
}

func TestStructFieldsKeepsAPlanPerTag(t *testing.T) {
	type in struct {
		Value string `form:"body" query:"url"`
	}

	var fromQuery in
	if _, err := decodeValues(url.Values{"url": {"q"}, "body": {"f"}}, &fromQuery, "query", false); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fromQuery.Value != "q" {
		t.Errorf("query bind read %q, want the query tag", fromQuery.Value)
	}

	var fromForm in
	if _, err := decodeValues(url.Values{"url": {"q"}, "body": {"f"}}, &fromForm, "form", false); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fromForm.Value != "f" {
		t.Errorf("form bind read %q, want the form tag", fromForm.Value)
	}
}

func TestStructFieldsCachesThePlan(t *testing.T) {
	type cached struct {
		Page int `query:"page"`
	}
	rt := reflect.TypeFor[cached]()
	first := structFields(rt, "query")
	second := structFields(rt, "query")
	if len(first) != 1 || first[0].keys[0] != "page" {
		t.Fatalf("plan = %+v", first)
	}
	if &first[0] != &second[0] {
		t.Error("structFields built the plan twice for the same type and tag")
	}
}

func TestFieldKeys(t *testing.T) {
	tests := []struct {
		name   string
		tag    string
		goName string
		want   []string
	}{
		{name: "an untagged field", goName: "Page", want: []string{"Page", "page"}},
		{
			name: "a tag and a field name", tag: "page", goName: "Page",
			want: []string{"page", "Page"},
		},
		{
			name: "a header tag", tag: "x-request-id", goName: "RequestID",
			want: []string{"x-request-id", "X-Request-Id", "RequestID", "requestid", "Requestid"},
		},
		{
			name: "a compound field name", tag: "IsAdmin", goName: "IsAdmin",
			want: []string{"IsAdmin", "isadmin", "Isadmin"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fieldKeys(tt.tag, tt.goName); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fieldKeys(%q, %q) = %q, want %q", tt.tag, tt.goName, got, tt.want)
			}
		})
	}
}

func TestLookupValues(t *testing.T) {
	tests := []struct {
		name string
		vals url.Values
		keys []string
		want string
		ok   bool
	}{
		{
			name: "the key as written",
			vals: url.Values{"page": {"1"}},
			keys: []string{"page", "Page"},
			want: "page", ok: true,
		},
		{
			name: "a later key",
			vals: url.Values{"X-Request-Id": {"abc"}},
			keys: []string{"x-request-id", "X-Request-Id"},
			want: "X-Request-Id", ok: true,
		},
		{
			name: "nothing",
			vals: url.Values{"other": {"1"}},
			keys: []string{"page"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, v, ok := lookupValues(tt.vals, tt.keys)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if key != tt.want {
				t.Errorf("key = %q, want %q", key, tt.want)
			}
			if ok && len(v) == 0 {
				t.Error("lookupValues answered with no values")
			}
		})
	}
}

func TestFieldNameReadsTheTags(t *testing.T) {
	type sample struct {
		Tagged   string `query:"q" json:"ignored"`
		JSONOnly string `json:"j"`
		Skipped  string `query:"-"`
		Bare     string
		Dated    time.Time `query:"d" format:"2006-01-02"`
	}
	rt := reflect.TypeFor[sample]()

	tests := []struct {
		field  string
		name   string
		layout string
		tagged bool
		skip   bool
	}{
		{field: "Tagged", name: "q", tagged: true},
		{field: "JSONOnly", name: "j", tagged: true},
		{field: "Skipped", skip: true, tagged: true},
		{field: "Bare", name: "Bare"},
		{field: "Dated", name: "d", layout: "2006-01-02", tagged: true},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			ft, ok := rt.FieldByName(tt.field)
			if !ok {
				t.Fatalf("no field %s", tt.field)
			}
			name, layout, tagged, skip := fieldName(ft, "query")
			if name != tt.name || layout != tt.layout || tagged != tt.tagged || skip != tt.skip {
				t.Errorf("fieldName = %q, %q, %v, %v; want %q, %q, %v, %v",
					name, layout, tagged, skip, tt.name, tt.layout, tt.tagged, tt.skip)
			}
		})
	}
}

func TestBuildFieldsSkipsUnexportedFields(t *testing.T) {
	type sample struct {
		Kept   string `query:"kept"`
		hidden string `query:"hidden"` //nolint:unused // The decoder must leave it alone.
	}
	plan := buildFields(reflect.TypeFor[sample](), "query")
	if len(plan) != 1 || plan[0].keys[0] != "kept" {
		t.Errorf("plan = %+v, want the exported field alone", plan)
	}
}

func TestDecodeValuesFillsAnEmbeddedPointer(t *testing.T) {
	type Page struct {
		Offset int `query:"offset"`
	}
	type filter struct {
		*Page
		Term string `query:"q"`
	}

	var got filter
	fields, err := decodeValues(url.Values{"offset": {"40"}, "q": {"go"}}, &got, "query", false)
	if err != nil || len(fields) != 0 {
		t.Fatalf("decode: %v, %+v", err, fields)
	}
	if got.Page == nil {
		t.Fatal("the embedded pointer is still nil")
	}
	if got.Offset != 40 || got.Term != "go" {
		t.Errorf("got %+v", *got.Page)
	}
}

func TestDecodeValuesLeavesAnUnusedEmbeddedPointerNil(t *testing.T) {
	type Page struct {
		Offset int `query:"offset"`
	}
	type filter struct {
		*Page
		Term string `query:"q"`
	}

	var got filter
	fields, err := decodeValues(url.Values{"q": {"go"}}, &got, "query", false)
	if err != nil || len(fields) != 0 {
		t.Fatalf("decode: %v, %+v", err, fields)
	}
	if got.Page != nil {
		t.Errorf("Page = %+v, want nil when none of its fields is present", got.Page)
	}
	if got.Term != "go" {
		t.Errorf("Term = %q, want go", got.Term)
	}
}

func TestDecodeValuesStopsAtARecursiveEmbeddedPointer(t *testing.T) {
	type Node struct {
		*Node
		Value string `query:"value"`
	}

	var got Node
	fields, err := decodeValues(url.Values{"value": {"root"}}, &got, "query", false)
	if err != nil || len(fields) != 0 {
		t.Fatalf("decode: %v, %+v", err, fields)
	}
	if got.Node != nil {
		t.Errorf("embedded Node = %+v, want the recursive edge left nil", got.Node)
	}
	if got.Value != "root" {
		t.Errorf("Value = %q, want root", got.Value)
	}
}

func TestDecodeValuesStopsAtAMutualEmbeddingCycle(t *testing.T) {
	var got MutuallyEmbeddedA
	fields, err := decodeValues(url.Values{"name": {"leaf"}}, &got, "query", false)
	if err != nil || len(fields) != 0 {
		t.Fatalf("decode: %v, %+v", err, fields)
	}
	if got.MutuallyEmbeddedB == nil || got.Name != "leaf" {
		t.Fatalf("B = %+v, want the populated non-cyclic descendant", got.MutuallyEmbeddedB)
	}
	if got.MutuallyEmbeddedA != nil {
		t.Errorf("cycle edge = %+v, want nil", got.MutuallyEmbeddedA)
	}
}

type MutuallyEmbeddedA struct {
	*MutuallyEmbeddedB
}

type MutuallyEmbeddedB struct {
	*MutuallyEmbeddedA
	Name string `query:"name"`
}

func TestDecodeValuesLeavesAnUnexportedEmbeddedPointerAlone(t *testing.T) {
	type filter struct {
		*hiddenPage
		Term string `query:"q"`
	}

	var got filter
	fields, err := decodeValues(url.Values{"offset": {"40"}, "q": {"go"}}, &got, "query", false)
	if err != nil || len(fields) != 0 {
		t.Fatalf("decode: %v, %+v", err, fields)
	}
	if got.hiddenPage != nil {
		t.Errorf("hiddenPage = %+v, want it left alone", got.hiddenPage)
	}
	if got.Term != "go" {
		t.Errorf("Term = %q, want the rest of the struct to be filled", got.Term)
	}
}

type hiddenPage struct {
	Offset int `query:"offset"`
}
