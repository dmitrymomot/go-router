package useragent_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/useragent"
)

func chRouter(mw router.Middleware[*appContext]) *router.Router[*appContext] {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(mw)
	r.GET("/", func(c *appContext) error {
		c.Response().Header().Add("Accept-CH", "DPR") // a later Add must not reach another answer
		return c.String(http.StatusOK, "ok")
	})
	r.GET("/healthz", func(c *appContext) error { return c.String(http.StatusOK, "ok") })
	return r
}

func TestAcceptCHAsksForTheHintsOfParseHeaders(t *testing.T) {
	r := chRouter(useragent.AcceptCH[*appContext])
	for range 2 {
		res := routertest.Get(r, "/")
		want := "Sec-CH-UA-Platform-Version, Sec-CH-UA-Full-Version-List, Sec-CH-UA-Model"
		if got := res.Header.Values("Accept-CH"); len(got) != 2 || got[0] != want || got[1] != "DPR" {
			t.Fatalf("Accept-CH = %q, want %q and the handler's own", got, want)
		}
		if got := strings.Join(res.Header.Values("Vary"), ","); got != "Sec-CH-UA-Platform-Version,Sec-CH-UA-Full-Version-List,Sec-CH-UA-Model" {
			t.Errorf("Vary = %q, want every hint", got)
		}
		if res.Header.Get("Critical-CH") != "" {
			t.Error("Critical-CH went out without Critical")
		}
	}
}

func TestAcceptCHWithConfig(t *testing.T) {
	r := chRouter(useragent.AcceptCHWithConfig(useragent.AcceptCHConfig[*appContext]{
		Hints:    []string{useragent.HeaderSecCHUAModel},
		Critical: true,
		Skip:     func(c *appContext) bool { return c.Path() == "/healthz" },
	}))
	res := routertest.Get(r, "/")
	if res.Header.Values("Accept-CH")[0] != "Sec-CH-UA-Model" || res.Header.Get("Critical-CH") != "Sec-CH-UA-Model" || res.Header.Get("Vary") != "Sec-CH-UA-Model" {
		t.Errorf("headers = %v, want the model in Accept-CH, Critical-CH and Vary", res.Header)
	}
	if res := routertest.Get(r, "/healthz"); res.Header.Get("Accept-CH") != "" || res.Header.Get("Vary") != "" {
		t.Errorf("a skipped request got %v", res.Header)
	}
}

func TestAcceptCHRefusesABadHint(t *testing.T) {
	for name, hints := range map[string][]string{
		"empty list":  {},
		"empty name":  {""},
		"two in one":  {"Sec-CH-UA-Model, DPR"},
		"with spaces": {"Sec CH"},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if msg, _ := recover().(string); !strings.HasPrefix(msg, "useragent: AcceptCHConfig.Hints") {
					t.Errorf("panic = %q, want a refused hint", msg)
				}
			}()
			useragent.AcceptCHWithConfig(useragent.AcceptCHConfig[*appContext]{Hints: hints})
		})
	}
}
