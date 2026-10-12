package router

import (
	"net/http"
	"testing"
)

func TestETagOf(t *testing.T) {
	a, b := ETagOf([]byte("hello")), ETagOf([]byte("hello"))
	if a != b || ETagOf([]byte("world")) == a {
		t.Errorf("tags: %s %s", a, ETagOf([]byte("world")))
	}
	if len(a) != 24 || a[0] != '"' || a[23] != '"' {
		t.Errorf("tag = %s", a)
	}
}

func TestCheckETag(t *testing.T) {
	const tag = `"abc"`
	tests := []struct {
		name, method, inm string
		want              bool
	}{
		{"match", "GET", `"abc"`, true},
		{"head", "HEAD", `"abc"`, true},
		{"weak request", "GET", `W/"abc"`, true},
		{"list", "GET", `"x", W/"abc" ,"y"`, true},
		{"star", "GET", `*`, true},
		{"no match", "GET", `"abd"`, false},
		{"none", "GET", ``, false},
		{"prefix is not a match", "GET", `"ab"`, false},
		{"junk", "GET", `abc`, false},
		{"unterminated", "GET", `"abc`, false},
		{"post ignores", "POST", `"abc"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRouter()
			var got bool
			r.Handle(tt.method, "/", func(c *tctx) error {
				if got = c.CheckETag(tag); got {
					return nil
				}
				return c.String(http.StatusOK, "body")
			})
			headers := map[string]string{}
			if tt.inm != "" {
				headers[HeaderIfNoneMatch] = tt.inm
			}
			rec := doWithHeaders(r, tt.method, "/", headers)
			if got != tt.want {
				t.Fatalf("CheckETag = %v, want %v", got, tt.want)
			}
			if rec.Header().Get(HeaderETag) != tag {
				t.Errorf("ETag = %q", rec.Header().Get(HeaderETag))
			}
			if tt.want && (rec.Code != 304 || rec.Body.Len() != 0) {
				t.Errorf("status %d body %q", rec.Code, rec.Body)
			}
			if !tt.want && rec.Code != 200 {
				t.Errorf("status = %d", rec.Code)
			}
		})
	}
}
