package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func TestHeartbeatAnswersAnUnregisteredPath(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Heartbeat[*appContext]("/ping"))
	r.GET("/", hello)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := do(r, httptest.NewRequest(method, "/ping", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s /ping = %d, want 200", method, rec.Code)
		}
		if ct := rec.Header().Get(router.HeaderContentType); ct != "text/plain" {
			t.Errorf("Content-Type = %q", ct)
		}
		wantBody := "."
		if method == http.MethodHead {
			wantBody = ""
		}
		if rec.Body.String() != wantBody {
			t.Errorf("%s body = %q, want %q", method, rec.Body, wantBody)
		}
	}
	if rec := get(r, "/ping/"); rec.Code != http.StatusNotFound {
		t.Errorf("/ping/ = %d, want 404", rec.Code)
	}
	if rec := do(r, httptest.NewRequest(http.MethodPost, "/ping", nil)); rec.Code == http.StatusOK {
		t.Error("POST /ping answered 200")
	}
}

func TestHeartbeatPanicsWithoutALeadingSlash(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("did not panic")
		}
	}()
	middleware.Heartbeat[*appContext]("ping")
}
