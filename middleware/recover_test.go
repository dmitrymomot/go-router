package middleware_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router/middleware"
)

func TestRecoverTurnsAPanicIntoA500(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Recover().Middleware)
	r.GET("/boom", func(*appContext) error { panic("handler exploded") })

	rec := get(r, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "exploded") {
		t.Errorf("the body leaks the panic value: %q", rec.Body.String())
	}
}

func TestRecoverPassesOnErrAbortHandler(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Recover().Middleware)
	r.GET("/abort", func(*appContext) error { panic(http.ErrAbortHandler) })

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	get(r, "/abort")
}

func TestRecoverSkip(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Recover(middleware.RecoverConfig{Skip: skipPath("/boom")}).Middleware)
	r.GET("/boom", func(*appContext) error { panic("handler exploded") })

	// The router still catches the panic that this middleware passed over.
	rec := get(r, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
