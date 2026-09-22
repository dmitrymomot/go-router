package router

import (
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"uuid"
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

func FuzzBuiltinClassesAgreeWithTheirRegexp(f *testing.F) {
	for _, seed := range []string{
		"0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d", "0198C5B6-3F0E-7B3A-9C1D-2F4E6A8B0C1D",
		"0198c5b63f0e7b3a9c1d2f4e6a8b0c1d", "", "-a", "a--b", "\u0663", "007",
	} {
		f.Add(seed)
	}
	classes := []struct {
		name  string
		match func(string) bool
		re    *regexp.Regexp
	}{
		{"uuid", isUUID, regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)},
		{"int", isDigits, regexp.MustCompile(`^[0-9]+$`)},
		{"slug", isSlug, regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)},
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, c := range classes {
			if got, want := c.match(s), c.re.MatchString(s); got != want {
				t.Errorf("%s(%q) = %v, the regexp says %v", c.name, s, got, want)
			}
		}
	})
}

func FuzzUUIDClassAdmitsOnlyWhatParses(f *testing.F) {
	for _, seed := range []string{
		"0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d", "0198c5b63f0e7b3a9c1d2f4e6a8b0c1d",
		"{0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d}", "urn:uuid:0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d", "new",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v string) {
		var parseErr error
		r := newTestRouter()
		r.GET("/u/{id:uuid}", func(c *tctx) error {
			_, parseErr = c.ParamAs[uuid.UUID]("id")
			return c.NoContent(http.StatusOK)
		})
		if do(r, http.MethodGet, "/u/"+url.PathEscape(v)).Code != http.StatusOK {
			return
		}
		if parseErr != nil {
			t.Fatalf("the class admitted %q, which ParamAs refused: %v", v, parseErr)
		}
		if _, err := uuid.Parse(v); err != nil {
			t.Fatalf("the class admitted %q, which uuid.Parse refused: %v", v, err)
		}
	})
}
