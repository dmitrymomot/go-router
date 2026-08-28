package middleware_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func recoverRouter(cfg middleware.RecoverConfig, caught **router.PanicValue) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.RecoverWithConfig[*appContext](cfg))
	r.ErrorHandler(func(c *appContext, err error) {
		if pv, ok := errors.AsType[*router.PanicValue](err); ok {
			*caught = pv
		}
		router.DefaultErrorHandler(c, err)
	})
	r.GET("/boom", func(*appContext) error { panic("handler exploded") })
	return r
}

func TestRecoverTurnsAPanicIntoA500(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Recover[*appContext])
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
	r.Use(middleware.Recover[*appContext])
	r.GET("/abort", func(*appContext) error { panic(http.ErrAbortHandler) })

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	get(r, "/abort")
}

func TestRecoverKeepsTheStackOfThePanic(t *testing.T) {
	var caught *router.PanicValue
	r := recoverRouter(middleware.RecoverConfig{}, &caught)

	get(r, "/boom")
	if caught == nil {
		t.Fatal("the error carries no panic value")
	}
	if caught.Value != "handler exploded" {
		t.Errorf("value = %v, want the value that reached panic", caught.Value)
	}
	if len(caught.Stack) == 0 {
		t.Fatal("the error carries no stack")
	}
	if !strings.Contains(string(caught.Stack), "goroutine") {
		t.Errorf("stack = %q, want the trace of the goroutine that panicked", caught.Stack)
	}
}

func TestRecoverStackSizeBoundsTheTrace(t *testing.T) {
	var caught *router.PanicValue
	r := recoverRouter(middleware.RecoverConfig{StackSize: 128}, &caught)

	get(r, "/boom")
	if caught == nil {
		t.Fatal("the error carries no panic value")
	}
	if len(caught.Stack) == 0 || len(caught.Stack) > 128 {
		t.Errorf("the stack is %d bytes, want it kept inside the 128 that the config asked for",
			len(caught.Stack))
	}
}

func TestRecoverDisableStackKeepsTheValue(t *testing.T) {
	var caught *router.PanicValue
	r := recoverRouter(middleware.RecoverConfig{DisableStack: true}, &caught)

	rec := get(r, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if caught == nil {
		t.Fatal("the error carries no panic value")
	}
	if len(caught.Stack) != 0 {
		t.Errorf("stack = %q, want none", caught.Stack)
	}
	if caught.Value != "handler exploded" {
		t.Errorf("value = %v, want the value that reached panic", caught.Value)
	}
	if caught.Unwrap() == nil {
		t.Error("the panic value unwraps to nothing, so errors.Is reaches no cause")
	}
}

func TestRecoverSkip(t *testing.T) {
	r := newRouter()
	r.Use(middleware.RecoverWithConfig[*appContext](middleware.RecoverConfig{Skip: skipPath("/boom")}))
	r.GET("/boom", func(*appContext) error { panic("handler exploded") })

	rec := get(r, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
