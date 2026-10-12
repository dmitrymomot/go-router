package profiler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware/profiler"
)

type appContext struct{ router.Base }

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHandler(t *testing.T) {
	h := profiler.Handler()
	tests := []struct {
		target string
		code   int
		body   string
	}{
		{"/pprof/", http.StatusOK, "Types of profiles"},
		{"/pprof/goroutine?debug=1", http.StatusOK, "goroutine profile"},
		{"/pprof/cmdline", http.StatusOK, ""},
		{"/vars", http.StatusOK, "memstats"},
		{"/pprof", http.StatusMovedPermanently, ""},
		{"/nope", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			rec := get(h, tt.target)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.body) {
				t.Errorf("got %d %.80q, want %d containing %q", rec.Code, rec.Body, tt.code, tt.body)
			}
		})
	}
}

func TestHandlerMounted(t *testing.T) {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.MountHandler("/debug", profiler.Handler())
	for target, want := range map[string]string{
		"/debug/pprof/":             "Types of profiles",
		"/debug/pprof/heap?debug=1": "heap profile",
		"/debug/vars":               "memstats",
	} {
		if rec := get(r, target); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s = %d %.80q, want %q", target, rec.Code, rec.Body, want)
		}
	}
	if rec := get(r, "/debug/pprof"); rec.Header().Get("Location") != "pprof/" {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
}
