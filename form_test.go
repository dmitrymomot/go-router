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
		{name: "a checked checkbox", field: "Bool", in: "on", want: true},
		{name: "on in capitals", field: "Bool", in: "ON", want: true},
		{name: "on in title case", field: "Bool", in: "On", want: true},
		{name: "off", field: "Bool", in: "off", want: false},
		{name: "off in capitals", field: "Bool", in: "OFF", want: false},
		{name: "on in mixed case", field: "Bool", in: "oN", bad: true},
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

func TestSetScalarLeavesAPointerNilOnAParseFailure(t *testing.T) {
	var dst struct {
		Page  *int
		Deep  **int
		Since *time.Time
	}
	rv := reflect.ValueOf(&dst).Elem()

	if err := setScalar(rv.Field(0), "abc", ""); err == nil {
		t.Error("setScalar(*int, abc) reported no error")
	}
	if dst.Page != nil {
		t.Errorf("Page = %d, want nil after a parse failure", *dst.Page)
	}
	if err := setScalar(rv.Field(1), "abc", ""); err == nil {
		t.Error("setScalar(**int, abc) reported no error")
	}
	if dst.Deep != nil {
		t.Errorf("Deep = %v, want nil after a parse failure", dst.Deep)
	}
	if err := setScalar(rv.Field(2), "not a date", time.DateOnly); err == nil {
		t.Error("setScalar(*time.Time, not a date) reported no error")
	}
	if dst.Since != nil {
		t.Errorf("Since = %v, want nil after a parse failure", dst.Since)
	}

	if p, err := ParseValue[*int]("abc"); err == nil || p != nil {
		t.Errorf("ParseValue[*int](abc) = %v, %v, want nil and an error", p, err)
	}
	if p, err := ParseValue[*int]("7"); err != nil || p == nil || *p != 7 {
		t.Errorf("ParseValue[*int](7) = %v, %v, want a pointer to 7", p, err)
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
	fields := decode(url.Values{"data": {"abc"}}, &got, "query")
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

			fields := decode(url.Values{"since": {tt.in}}, dst.Interface(), "query")
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
	fields := decode(url.Values{"since": {"2026-01-02T03:04:05Z"}}, &got, "query")
	if len(fields) != 0 {
		t.Fatalf("fields = %+v, want none", fields)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !got.Since.Equal(want) {
		t.Errorf("Since = %v, want %v", got.Since, want)
	}

	fields = decode(url.Values{"since": {"2026-01-02"}}, &got, "query")
	if len(fields) != 1 {
		t.Errorf("fields = %+v, want one", fields)
	}
}

func TestDecodeValuesReadsAFormatTagForASliceAndAPointer(t *testing.T) {
	var got struct {
		Days []time.Time `query:"day" format:"2006-01-02"`
		Cut  *time.Time  `query:"cut" format:"2006-01-02"`
	}
	fields := decode(url.Values{
		"day": {"2026-01-02", "2026-01-03"},
		"cut": {"2026-02-01"},
	}, &got, "query")
	if len(fields) != 0 {
		t.Fatalf("fields = %+v, want none", fields)
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
	fields := decode(url.Values{
		"page":  {"a"},
		"limit": {"b"},
		"ttl":   {"c"},
		"q":     {"go"},
	}, &got, "query")
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

func TestDecodeValuesMatchesTheTagExactly(t *testing.T) {
	var got struct {
		Page int `query:"page"`
	}
	if fields := decode(url.Values{"Page": {"1"}, "PAGE": {"2"}}, &got, "query"); len(fields) != 0 || got.Page != 0 {
		t.Fatalf("Page = %d, fields = %+v, want another spelling of the tag left alone", got.Page, fields)
	}
	fields := decode(url.Values{"page": {"a"}}, &got, "query")
	if len(fields) != 1 || fields[0].Field != "page" {
		t.Fatalf("fields = %+v, want the key of the tag", fields)
	}
}

func TestDecodeValuesSkipsADashTag(t *testing.T) {
	var got struct {
		Ignored string `query:"-"`
		Kept    string `query:"kept"`
	}
	decode(url.Values{"Ignored": {"x"}, "-": {"x"}, "kept": {"y"}}, &got, "query")
	if got.Ignored != "" || got.Kept != "y" {
		t.Errorf("got %+v", got)
	}
}

func TestStructFieldsKeepsAPlanPerTag(t *testing.T) {
	type in struct {
		Value string `form:"body" query:"url"`
	}

	var fromQuery in
	decode(url.Values{"url": {"q"}, "body": {"f"}}, &fromQuery, "query")
	if fromQuery.Value != "q" {
		t.Errorf("query bind read %q, want the query tag", fromQuery.Value)
	}

	var fromForm in
	decode(url.Values{"url": {"q"}, "body": {"f"}}, &fromForm, "form")
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
	if len(first) != 1 || first[0].key != "page" {
		t.Fatalf("plan = %+v", first)
	}
	if &first[0] != &second[0] {
		t.Error("structFields built the plan twice for the same type and tag")
	}
}

func TestBuildFieldsReadsOnlyItsOwnTag(t *testing.T) {
	type Embedded struct {
		Inner string `query:"inner"`
	}
	type sample struct {
		Embedded
		Tagged   string `query:"q" json:"ignored"`
		JSONOnly string `json:"j"`
		Other    string `form:"other"`
		Skipped  string `query:"-"`
		Unnamed  string `query:",omitempty"`
		Bare     string
		Dated    time.Time `query:"d" format:"2006-01-02"`
	}

	want := []fieldInfo{
		{index: 0, embedded: true},
		{key: "q", index: 1},
		{key: "d", layout: "2006-01-02", index: 7},
	}
	if got := buildFields(reflect.TypeFor[sample](), "query"); !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %+v, want %+v", got, want)
	}
}

func TestBuildFieldsCanonicalizesAHeaderName(t *testing.T) {
	type sample struct {
		RequestID string `header:"x-request-id"`
	}
	plan := buildFields(reflect.TypeFor[sample](), "header")
	if len(plan) != 1 || plan[0].key != "X-Request-Id" {
		t.Errorf("plan = %+v, want the canonical header name", plan)
	}
}

func TestBuildFieldsSkipsUnexportedFields(t *testing.T) {
	type sample struct {
		Kept   string `query:"kept"`
		hidden string `query:"hidden"` //nolint:unused // The decoder must leave it alone.
	}
	plan := buildFields(reflect.TypeFor[sample](), "query")
	if len(plan) != 1 || plan[0].key != "kept" {
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
	fields := decode(url.Values{"offset": {"40"}, "q": {"go"}}, &got, "query")
	if len(fields) != 0 {
		t.Fatalf("fields = %+v, want none", fields)
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
	fields := decode(url.Values{"q": {"go"}}, &got, "query")
	if len(fields) != 0 {
		t.Fatalf("fields = %+v, want none", fields)
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
	fields := decode(url.Values{"value": {"root"}}, &got, "query")
	if len(fields) != 0 {
		t.Fatalf("fields = %+v, want none", fields)
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
	fields := decode(url.Values{"name": {"leaf"}}, &got, "query")
	if len(fields) != 0 {
		t.Fatalf("fields = %+v, want none", fields)
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
	fields := decode(url.Values{"offset": {"40"}, "q": {"go"}}, &got, "query")
	if len(fields) != 0 {
		t.Fatalf("fields = %+v, want none", fields)
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

func decode(vals url.Values, dst any, tag string) []FieldError {
	return decodeValues(vals, reflect.ValueOf(dst).Elem(), tag)
}
