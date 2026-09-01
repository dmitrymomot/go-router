package router

import (
	"net/http"
	"net/url"
	"testing"
)

func FuzzEscapedParamIsDecodedExactlyOnce(f *testing.F) {
	for _, seed := range []string{"plain", "a/b", "%2F", "1", "hello world"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if value == "" {
			t.Skip()
		}
		r := newTestRouter()
		r.GET("/value/{value}", func(c *tctx) error {
			return c.String(http.StatusOK, c.Param("value"))
		})
		target := "/value/" + url.PathEscape(value)
		rec := do(r, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %q status = %d", target, rec.Code)
		}
		if got := rec.Body.String(); got != value {
			t.Fatalf("Param = %q, want %q", got, value)
		}
	})
}
