package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// lazyCSRFRouter has a page that never reads the token, a form that does, and
// a POST target.
func lazyCSRFRouter() *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.CSRF[*appContext])
	r.GET("/plain", func(c *appContext) error { return c.String(http.StatusOK, "no form here") })
	form := func(c *appContext) error {
		return c.String(http.StatusOK, middleware.CSRFTokenFrom(c))
	}
	r.GET("/", form)
	r.GET("/form", form)
	r.POST("/save", func(c *appContext) error { return c.String(http.StatusOK, "saved") })
	return r
}

func TestCSRFSafeRequestThatSkipsTheTokenGetsNoCookieAndNoVary(t *testing.T) {
	rec := get(lazyCSRFRouter(), "/plain")
	if got := rec.Result().Cookies(); len(got) != 0 {
		t.Errorf("the answer set %d cookies, want none", len(got))
	}
	if got := rec.Header().Values(router.HeaderVary); containsFold(got, router.HeaderCookie) {
		t.Errorf("vary = %q, want no Cookie", got)
	}
}

func TestCSRFReadingTheTokenIssuesTheCookieAndVary(t *testing.T) {
	rec := get(lazyCSRFRouter(), "/form")
	csrfCookie(t, rec)
	if got := rec.Header().Values(router.HeaderVary); !containsFold(got, router.HeaderCookie) {
		t.Errorf("vary = %q, want Cookie", got)
	}
}

func TestCSRFReadingTheTokenWithACookieSetsNoCookieButVaries(t *testing.T) {
	r := lazyCSRFRouter()
	_, cookie := csrfSession(t, r)
	rec := do(r, withCookie(httptest.NewRequest(http.MethodGet, "/form", nil), cookie))
	if got := rec.Result().Cookies(); len(got) != 0 {
		t.Errorf("the answer set %d cookies, want none", len(got))
	}
	if got := rec.Header().Values(router.HeaderVary); !containsFold(got, router.HeaderCookie) {
		t.Errorf("vary = %q, want Cookie", got)
	}
}

func TestCSRFUnsafeRequestIssuesTheCookieAndVary(t *testing.T) {
	rec := do(lazyCSRFRouter(), httptest.NewRequest(http.MethodPost, "/save", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	csrfCookie(t, rec)
	if got := rec.Header().Values(router.HeaderVary); !containsFold(got, router.HeaderCookie) {
		t.Errorf("vary = %q, want Cookie", got)
	}
}

func TestCSRFTokenFromOutsideTheMiddleware(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, "["+middleware.CSRFTokenFrom(c)+"]")
	})
	if got := get(r, "/").Body.String(); got != "[]" {
		t.Errorf("token = %q, want none", got)
	}
}

func TestCSRFMasksTheTokenOnEveryRender(t *testing.T) {
	r := newRouter()
	r.Use(middleware.CSRF[*appContext])
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, middleware.CSRFTokenFrom(c)+" "+middleware.CSRFTokenFrom(c))
	})
	rec := get(r, "/")
	a, b, ok := strings.Cut(rec.Body.String(), " ")
	if !ok || a == b {
		t.Fatalf("renders = %q and %q, want two that differ", a, b)
	}
	if unmaskToken(t, a) != unmaskToken(t, b) || unmaskToken(t, a) != csrfCookie(t, rec).Value {
		t.Error("the masks do not hide the same token")
	}
}

func TestCSRFAcceptsMaskedAndBareTokens(t *testing.T) {
	r := lazyCSRFRouter()
	masked, cookie := csrfSession(t, r)
	other, _ := csrfSession(t, r)
	// The last character of a masked token carries bits that decoding drops,
	// so alter the one before it.
	n := len(masked)
	bare := cookie.Value

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"masked", masked, http.StatusOK},
		{"bare", bare, http.StatusOK},
		{"masked, a character changed", masked[:n-2] + flip(masked[n-2]) + masked[n-1:], http.StatusForbidden},
		{"masked, pad changed", flip(masked[0]) + masked[1:], http.StatusForbidden},
		{"bare, last character changed", bare[:len(bare)-1] + flip(bare[len(bare)-1]), http.StatusForbidden},
		{"masked, truncated", masked[:n-1], http.StatusForbidden},
		{"masked of another session", other, http.StatusForbidden},
		{"not base64", "!" + masked[1:], http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := withCookie(httptest.NewRequest(http.MethodPost, "/save", nil), cookie)
			req.Header.Set(router.HeaderXCSRFToken, tt.token)
			if rec := do(r, req); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// flip returns a character of the base64url alphabet other than c.
func flip(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}
