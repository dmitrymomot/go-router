package pagination_test

import (
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/pagination"
	"github.com/dmitrymomot/go-router/routertest"
)

type appContext struct{ router.Base }

type listPage struct {
	pagination.Page
	Q string `query:"q"`
}

type listCursor struct {
	pagination.Cursor
}

// bind runs Bind[T] on a request for target, and reports what it bound.
func bind[T any](t *testing.T, target string) (T, error) {
	t.Helper()
	var got T
	var err error
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.GET("/", func(c *appContext) error {
		got, err = c.Bind[T]()
		return nil
	})
	routertest.Get(r, target)
	return got, err
}

func fields(err error) string {
	var names []string
	for _, f := range router.FieldErrorsOf(err) {
		names = append(names, f.Field)
	}
	return strings.Join(names, ",")
}

func TestPage(t *testing.T) {
	tests := []struct {
		name          string
		target        string
		limit, offset int32
		status        int
		fields        string
	}{
		{name: "defaults", target: "/", limit: 20, offset: 0},
		{name: "values", target: "/?page=3&per_page=15", limit: 15, offset: 30},
		{name: "the first of repeated values", target: "/?page=2&page=4&per_page=10&per_page=50", limit: 10, offset: 10},
		{name: "the last page that fits", target: "/?page=21474837&per_page=100", limit: 100, offset: 2147483600},
		{name: "page below 1", target: "/?page=0", status: http.StatusUnprocessableEntity, fields: "page"},
		{name: "negative values", target: "/?page=-4&per_page=0", status: http.StatusUnprocessableEntity, fields: "page,per_page"},
		{name: "per_page above the maximum", target: "/?page=2&per_page=999", status: http.StatusUnprocessableEntity, fields: "per_page"},
		{name: "an offset past int32", target: "/?page=2147483647&per_page=100", status: http.StatusUnprocessableEntity, fields: "page"},
		{name: "not a number", target: "/?page=two", status: http.StatusBadRequest, fields: "page"},
		{name: "an empty value", target: "/?page=", status: http.StatusUnprocessableEntity, fields: "page"},
		{name: "past int", target: "/?page=99999999999999999999", status: http.StatusBadRequest, fields: "page"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bind[listPage](t, tt.target)
			if tt.status != 0 {
				if router.StatusOf(err) != tt.status || fields(err) != tt.fields {
					t.Fatalf("err = %v with fields %q, want %d naming %q", err, fields(err), tt.status, tt.fields)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if l, o := got.Limit(100), got.Offset(100); l != tt.limit || o != tt.offset {
				t.Errorf("Limit, Offset = %d, %d; want %d, %d", l, o, tt.limit, tt.offset)
			}
		})
	}
}

func TestPageKeepsTheOffsetInStepWithTheCap(t *testing.T) {
	p := pagination.Page{Number: 3, PerPage: 80}
	if l, o := p.Limit(50), p.Offset(50); l != 50 || o != 100 {
		t.Errorf("Limit, Offset = %d, %d; want 50, 100", l, o)
	}
}

func TestLimitAndOffsetKeepToTheirRange(t *testing.T) {
	tests := []struct {
		name          string
		page          pagination.Page
		max           int
		limit, offset int32
	}{
		{"zero value", pagination.Page{}, 0, pagination.DefaultPerPage, 0},
		{"zero value under a small cap", pagination.Page{}, 5, 5, 0},
		{"a cap above the maximum", pagination.Page{Number: 1, PerPage: 1000}, 500, pagination.MaxPerPage, 0},
		{"negative page", pagination.Page{Number: -9, PerPage: 10}, 100, 10, 0},
		{"a page past int32", pagination.Page{Number: math.MaxInt, PerPage: 100}, 100, 100, math.MaxInt32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if l, o := tt.page.Limit(tt.max), tt.page.Offset(tt.max); l != tt.limit || o != tt.offset {
				t.Errorf("Limit, Offset = %d, %d; want %d, %d", l, o, tt.limit, tt.offset)
			}
		})
	}
}

func TestCursor(t *testing.T) {
	tests := []struct {
		name   string
		target string
		after  string
		limit  int32
		status int
	}{
		{name: "defaults", target: "/", limit: 20},
		{name: "values", target: "/?after=" + url.QueryEscape("eyJpZCI6NDJ9/+") + "&limit=5", after: "eyJpZCI6NDJ9/+", limit: 5},
		{name: "limit below 1", target: "/?limit=0", status: http.StatusUnprocessableEntity},
		{name: "limit above the maximum", target: "/?limit=101", status: http.StatusUnprocessableEntity},
		{name: "not a number", target: "/?limit=all", status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bind[listCursor](t, tt.target)
			if tt.status != 0 {
				if router.StatusOf(err) != tt.status || fields(err) != "limit" {
					t.Fatalf("err = %v, want %d naming limit", err, tt.status)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got.After != tt.after || got.Limit(100) != tt.limit {
				t.Errorf("got %+v with Limit %d, want after %q and %d", got.Cursor, got.Limit(100), tt.after, tt.limit)
			}
		})
	}
	if l := (pagination.Cursor{}).Limit(10); l != 10 {
		t.Errorf("the zero Cursor under a cap of 10: Limit = %d, want 10", l)
	}
}
