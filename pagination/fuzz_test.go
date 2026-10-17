package pagination_test

import (
	"net/url"
	"testing"

	"github.com/dmitrymomot/go-router/pagination"
)

func FuzzPage(f *testing.F) {
	for _, seed := range [][2]string{
		{"", ""},
		{"1", "20"},
		{"0", "0"},
		{"2147483647", "100"},
		{"21474837", "100"},
		{"wrong", "20"},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, page, perPage string) {
		q := url.Values{}
		q.Set("page", page)
		q.Set("per_page", perPage)
		p, err := bind[listPage](t, "/?"+q.Encode())
		if err != nil {
			return
		}
		limit, offset := p.Limit(pagination.MaxPerPage), p.Offset(pagination.MaxPerPage)
		if limit < 1 || limit > pagination.MaxPerPage {
			t.Fatalf("limit = %d, want 1 to %d", limit, pagination.MaxPerPage)
		}
		if offset < 0 || offset%limit != 0 {
			t.Fatalf("offset = %d, want a multiple of %d of zero or more", offset, limit)
		}
		if int64(offset) != int64(p.Number-1)*int64(p.PerPage) {
			t.Fatalf("offset = %d for %+v, want the rows of the pages before", offset, p.Page)
		}
	})
}

func FuzzLimitAndOffset(f *testing.F) {
	f.Add(0, 0, 0)
	f.Add(-1, -1, -1)
	f.Add(1<<62, 1<<62, 1<<62)
	f.Fuzz(func(t *testing.T, page, perPage, maxPerPage int) {
		p := pagination.Page{Number: page, PerPage: perPage}
		limit, offset := p.Limit(maxPerPage), p.Offset(maxPerPage)
		if limit < 1 || limit > pagination.MaxPerPage || offset < 0 {
			t.Fatalf("%+v under %d: Limit, Offset = %d, %d", p, maxPerPage, limit, offset)
		}
		if c := (pagination.Cursor{PerPage: perPage}).Limit(maxPerPage); c != limit {
			t.Fatalf("Cursor.Limit = %d, Page.Limit = %d", c, limit)
		}
	})
}
