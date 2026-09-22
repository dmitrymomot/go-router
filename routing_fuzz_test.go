package router

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
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

func FuzzExpandRoutesBack(f *testing.F) {
	for _, seed := range []string{"plain", "a/b", "%2F", "/evil.com", "//x", "a b", "web-api", "42", "Acme", "", "a.b", `\x`, "?#&="} {
		f.Add(seed)
	}
	echoV := func(c *tctx) error {
		return c.String(http.StatusOK, c.RoutePattern()+" v="+c.Param("v")+" q="+c.Query("v"))
	}
	// One router per pattern: Expand knows its own pattern and nothing of the
	// routes it competes with.
	paths := []string{"/v/{v}", "/t/{v}-x", "/r/{v...}", "/{v...}", "/n/{v:int}"}
	routers := make(map[string]*Router[*tctx], len(paths))
	for _, p := range paths {
		routers[p] = newTestRouter()
		routers[p].GET(p, echoV)
	}
	r := newTestRouter()
	r.GET("/q", echoV)
	r.Host("{v}.example.com", func(h *Router[*tctx]) { h.GET("/", echoV) })

	f.Fuzz(func(t *testing.T, v string) {
		for _, p := range paths {
			out, err := Expand(p, "v", v)
			if err != nil {
				continue
			}
			if strings.HasPrefix(out, "//") {
				t.Fatalf("Expand(%q, %q) = %q, a link to another host", p, v, out)
			}
			if p == "/n/{v:int}" && !isDigits(v) {
				t.Fatalf("Expand(%q, %q) = %q, but the class admits digits alone", p, v, out)
			}
			rec := do(routers[p], http.MethodGet, out)
			if want := p + " v=" + v + " q="; rec.Code != http.StatusOK || rec.Body.String() != want {
				t.Fatalf("Expand(%q, %q) = %q, which reached %d %q, want %q", p, v, out, rec.Code, rec.Body, want)
			}
		}

		out, err := Expand("/q?v={v}", "v", v)
		if err != nil {
			t.Fatalf("Expand(/q?v={v}, %q) = %v; a query value takes anything", v, err)
		}
		u, err := url.Parse(out)
		if err != nil || u.Query().Get("v") != v {
			t.Fatalf("Expand(/q?v={v}, %q) = %q, which reads back as %q (%v)", v, out, u.Query().Get("v"), err)
		}
		if rec := do(r, http.MethodGet, out); rec.Body.String() != "/q v= q="+v {
			t.Fatalf("GET %q reached %q", out, rec.Body)
		}

		host, err := Expand("{v}.example.com", "v", v)
		if err != nil {
			return
		}
		if want := "/ v=" + v + " q="; doHost(r, http.MethodGet, host, "/").Body.String() != want {
			t.Fatalf("Expand({v}.example.com, %q) = %q, which does not route back", v, host)
		}
	})
}
