package router

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type tctx struct {
	Base
	Tag string
}

func newTestRouter() *Router[*tctx] {
	return New(func(http.ResponseWriter, *http.Request) *tctx { return &tctx{Tag: "app"} })
}

func echoRoute(c *tctx) error {
	parts := []string{c.RoutePattern()}
	for _, n := range c.ParamNames() {
		parts = append(parts, n+"="+c.Param(n))
	}
	return c.String(http.StatusOK, strings.Join(parts, " "))
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// doBody is do with a request body. An empty contentType sends none, which is
// its own case: a body method that does not say what it carries.
func doBody(h http.Handler, method, target, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set(HeaderContentType, contentType)
	} else {
		req.Header.Del(HeaderContentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMatch(t *testing.T) {
	r := newTestRouter()
	for _, p := range []string{
		"/",
		"/users",
		"/users/new",
		"/users/{id}",
		"/users/{id}/posts/{postID}",
		"/a/{x}/c",
		"/a/b/d",
		"/files/{path...}",
		"/assets/*",
		"/orders/{id:[0-9]+}",
		"/orders/{id}/cancel",
	} {
		r.GET(p, echoRoute)
	}

	tests := []struct {
		path string
		want string
	}{
		{"/", "/"},
		{"/users", "/users"},
		{"/users/", "/users"},
		{"/users/new", "/users/new"},
		{"/users/42", "/users/{id} id=42"},
		{"/users/42/posts/7", "/users/{id}/posts/{postID} id=42 postID=7"},
		{"/a/b/c", "/a/{x}/c x=b"},
		{"/a/b/d", "/a/b/d"},
		{"/a/z/c", "/a/{x}/c x=z"},
		{"/files/a/b/c.txt", "/files/{path...} path=a/b/c.txt"},
		{"/files", "/files/{path...} path="},
		{"/assets/css/app.css", "/assets/* *=css/app.css"},
		{"/orders/17", "/orders/{id:[0-9]+} id=17"},
		{"/orders/17/cancel", "/orders/{id}/cancel id=17"},
		{"/orders/abc/cancel", "/orders/{id}/cancel id=abc"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := do(r, http.MethodGet, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegexParamFallsBackToPlainParam(t *testing.T) {
	r := newTestRouter()
	r.GET("/orders/{id:[0-9]+}", func(c *tctx) error { return c.String(200, "numeric "+c.Param("id")) })
	r.GET("/orders/{id}", func(c *tctx) error { return c.String(200, "any "+c.Param("id")) })

	if got := do(r, http.MethodGet, "/orders/12").Body.String(); got != "numeric 12" {
		t.Errorf("numeric route: %q", got)
	}
	if got := do(r, http.MethodGet, "/orders/ab").Body.String(); got != "any ab" {
		t.Errorf("fallback route: %q", got)
	}
}

func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	r := newTestRouter()
	r.GET("/users", echoRoute)
	r.POST("/users", echoRoute)

	if rec := do(r, http.MethodGet, "/missing"); rec.Code != http.StatusNotFound {
		t.Errorf("missing path status = %d, want 404", rec.Code)
	}

	rec := do(r, http.MethodDelete, "/users")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get(HeaderAllow); got != "GET, HEAD, OPTIONS, POST" {
		t.Errorf("Allow = %q", got)
	}
}

func TestAllowHeaderNamesExactlyTheMethodsThatAnswer(t *testing.T) {
	r := newTestRouter()
	r.GET("/users", echoRoute)
	r.POST("/users", echoRoute)
	r.PATCH("/users", echoRoute)

	rec := do(r, http.MethodDelete, "/users")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	allow := rec.Header().Get(HeaderAllow)
	named := strings.Split(allow, ", ")
	if want := []string{"GET", "HEAD", "OPTIONS", "PATCH", "POST"}; !slices.Equal(named, want) {
		t.Fatalf("Allow = %q, want %q", allow, strings.Join(want, ", "))
	}

	for _, m := range named {
		if got := do(r, m, "/users").Code; got == http.StatusMethodNotAllowed {
			t.Errorf("Allow names %s, but the router answers 405 to it", m)
		}
	}
	for _, m := range []string{http.MethodDelete, http.MethodPut, "PROPFIND"} {
		if got := do(r, m, "/users").Code; got != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405; Allow = %q leaves the method out", m, got, allow)
		}
	}
}

func TestASentinelRouteNeverProducesA405(t *testing.T) {
	r := newTestRouter()
	r.Any("/rpc", echoRoute)
	r.GET("/rpc", echoRoute)

	for _, m := range []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions, MethodQuery, "PROPFIND",
	} {
		rec := do(r, m, "/rpc")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", m, rec.Code)
		}
		if got := rec.Header().Get(HeaderAllow); got != "" {
			t.Errorf("%s: Allow = %q, want none", m, got)
		}
	}
}

func TestAutomaticHeadAndOptions(t *testing.T) {
	r := newTestRouter()
	r.GET("/users", func(c *tctx) error { return c.String(200, "list") })

	if rec := do(r, http.MethodHead, "/users"); rec.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", rec.Code)
	}

	rec := do(r, http.MethodOptions, "/users")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get(HeaderAllow); got != "GET, HEAD, OPTIONS" {
		t.Errorf("Allow = %q", got)
	}

	r2 := newTestRouter()
	r2.HandleOPTIONS(false)
	r2.GET("/users", echoRoute)
	if rec := do(r2, http.MethodOptions, "/users"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("disabled OPTIONS status = %d, want 405", rec.Code)
	} else if got := rec.Header().Get(HeaderAllow); got != "GET, HEAD" {
		t.Errorf("disabled OPTIONS Allow = %q, want %q", got, "GET, HEAD")
	}

	r3 := newTestRouter()
	r3.HandleOPTIONS(false)
	r3.GET("/users", echoRoute)
	r3.OPTIONS("/users", echoRoute)
	if rec := do(r3, http.MethodDelete, "/users"); rec.Header().Get(HeaderAllow) != "GET, HEAD, OPTIONS" {
		t.Errorf("explicit OPTIONS Allow = %q, want %q", rec.Header().Get(HeaderAllow), "GET, HEAD, OPTIONS")
	}
}

func TestRedirectTrailingSlash(t *testing.T) {
	r := newTestRouter()
	r.RedirectTrailingSlash(true)
	r.GET("/users", echoRoute)

	rec := do(r, http.MethodGet, "/users/?page=2")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get(HeaderLocation); got != "/users?page=2" {
		t.Errorf("Location = %q", got)
	}
}

func TestGroupRouteAndMiddlewareOrder(t *testing.T) {
	var order []string
	mark := func(name string) Middleware[*tctx] {
		return func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
			return func(c *tctx) error {
				order = append(order, name)
				return next(c)
			}
		}
	}

	r := newTestRouter()
	r.Use(mark("root"))
	r.Route("/admin", func(g *Router[*tctx]) {
		g.Use(mark("admin"))
		g.GET("/users", echoRoute, mark("route"))
	})
	r.GET("/open", echoRoute)

	if got := do(r, http.MethodGet, "/admin/users").Body.String(); got != "/admin/users" {
		t.Fatalf("body = %q", got)
	}
	if want := "root admin route"; strings.Join(order, " ") != want {
		t.Errorf("order = %q, want %q", strings.Join(order, " "), want)
	}

	order = nil
	do(r, http.MethodGet, "/open")
	if want := "root"; strings.Join(order, " ") != want {
		t.Errorf("order = %q, want %q", strings.Join(order, " "), want)
	}
}

func TestWithAddsMiddlewareToOneRoute(t *testing.T) {
	hit := 0
	count := func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error { hit++; return next(c) }
	}

	r := newTestRouter()
	r.With(count).POST("/login", echoRoute)
	r.GET("/health", echoRoute)

	do(r, http.MethodPost, "/login")
	do(r, http.MethodGet, "/health")
	if hit != 1 {
		t.Errorf("middleware ran %d times, want 1", hit)
	}
}

func TestMountSameContext(t *testing.T) {
	api := New(func(http.ResponseWriter, *http.Request) *tctx { return &tctx{} })
	api.GET("/users/{id}", echoRoute)

	r := newTestRouter()
	r.Route("/tenants/{tid}", func(g *Router[*tctx]) {
		g.Mount("/api", api)
	})

	rec := do(r, http.MethodGet, "/tenants/acme/api/users/7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), "/tenants/{tid}/api/users/{id} tid=acme id=7"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestMountHandlerStripsThePrefix(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "path=%s", r.URL.Path)
	})

	r := newTestRouter()
	r.MountHandler("/static", inner)

	for _, tc := range []struct{ target, want string }{
		{"/static/css/app.css", "path=/css/app.css"},
		{"/static", "path=/"},
		{"/static/", "path=/"},
	} {
		if got := do(r, http.MethodGet, tc.target).Body.String(); got != tc.want {
			t.Errorf("%s: body = %q, want %q", tc.target, got, tc.want)
		}
	}
}

func TestMountRouterWithAnotherContextType(t *testing.T) {
	type adminCtx struct {
		Base
		Role string
	}
	admin := New(func(http.ResponseWriter, *http.Request) *adminCtx {
		return &adminCtx{Role: "admin"}
	})
	admin.GET("/users/{id}", func(c *adminCtx) error {
		return c.String(http.StatusOK, c.Role+":"+c.Param("id")+":"+c.Path())
	})

	r := newTestRouter()
	r.MountRouter("/admin", admin)

	rec := do(r, http.MethodGet, "/admin/users/9")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), "admin:9:/users/9"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestEscapedParameter(t *testing.T) {
	r := newTestRouter()
	r.GET("/files/{name}", func(c *tctx) error { return c.String(200, c.Param("name")) })

	if got := do(r, http.MethodGet, "/files/a%2Fb").Body.String(); got != "a/b" {
		t.Errorf("param = %q, want %q", got, "a/b")
	}
}

func TestPercentEscapeCanonicalization(t *testing.T) {
	r := newTestRouter()
	r.GET("/alpha", func(c *tctx) error { return c.String(http.StatusOK, "alpha") })
	r.GET("/café", func(c *tctx) error { return c.String(http.StatusOK, "café") })
	r.GET("/slash/a%2Fb", func(c *tctx) error { return c.String(http.StatusOK, "escaped slash") })
	r.GET("/slash/a/b", func(c *tctx) error { return c.String(http.StatusOK, "split slash") })
	r.GET("/backslash/a%5Cb", func(c *tctx) error { return c.String(http.StatusOK, "escaped backslash") })
	r.GET("/value/{value}", func(c *tctx) error { return c.String(http.StatusOK, c.Param("value")) })

	tests := []struct{ path, want string }{
		{"/%61lpha", "alpha"},
		{"/caf%C3%A9", "café"},
		{"/caf%c3%a9", "café"},
		{"/slash/a%2Fb", "escaped slash"},
		{"/slash/a%2fb", "escaped slash"},
		{"/backslash/a%5Cb", "escaped backslash"},
		{"/backslash/a%5cb", "escaped backslash"},
		{"/value/a%2Fb", "a/b"},
		{"/value/a%5Cb", `a\b`},
		{"/value/%252F", "%2F"},
		{"/value/%255C", "%5C"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := do(r, http.MethodGet, tc.path)
			if rec.Code != http.StatusOK || rec.Body.String() != tc.want {
				t.Errorf("GET %s = %d %q, want %d %q", tc.path, rec.Code, rec.Body.String(), http.StatusOK, tc.want)
			}
		})
	}
}

func TestCanonicalEscapedPath(t *testing.T) {
	tests := []struct{ path, want string }{
		{"/plain", "/plain"},
		{"/%41lpha", "/Alpha"},
		{"/caf%c3%a9", "/café"},
		{"/a%2fb%5cc", "/a%2Fb%5Cc"},
		{"/%252f", "/%252f"},
		{"/%", "/%"},
		{"/%2", "/%2"},
		{"/%zz", "/%zz"},
	}
	for _, tc := range tests {
		if got := canonicalEscapedPath(tc.path); got != tc.want {
			t.Errorf("canonicalEscapedPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestErrorHandling(t *testing.T) {
	r := newTestRouter()
	r.GET("/boom", func(*tctx) error { return ErrForbidden.WithMessage("no entry") })
	r.GET("/internal", func(*tctx) error { return fmt.Errorf("database is down") })

	rec := do(r, http.MethodGet, "/boom")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if got, want := rec.Body.String(), "no entry"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set(HeaderAccept, "text/html,application/xhtml+xml")
	textRec := httptest.NewRecorder()
	r.ServeHTTP(textRec, req)
	if got := textRec.Body.String(); got != "no entry" {
		t.Errorf("text body = %q, want %q", got, "no entry")
	}

	rec = do(r, http.MethodGet, "/internal")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "database") {
		t.Errorf("body leaks the internal cause: %q", rec.Body.String())
	}
}

func TestRoutesIntrospection(t *testing.T) {
	r := newTestRouter()
	r.GET("/users", echoRoute)
	r.POST("/users", echoRoute)
	r.Route("/admin", func(g *Router[*tctx]) { g.GET("/stats", echoRoute) })

	got := r.Routes()
	want := []Route{
		{Method: http.MethodGet, Pattern: "/admin/stats"},
		{Method: http.MethodGet, Pattern: "/users"},
		{Method: http.MethodPost, Pattern: "/users"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d routes, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("route %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestPanics(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
		want string
	}{
		{"duplicate route", func() {
			r := newTestRouter()
			r.GET("/a", echoRoute)
			r.GET("/a", echoRoute)
			r.Routes()
		}, "already registered"},
		{"conflicting parameter names", func() {
			r := newTestRouter()
			r.GET("/users/{id}", echoRoute)
			r.GET("/users/{uid}/posts", echoRoute)
			r.Routes()
		}, "already named"},
		{"catch-all is not last", func() {
			r := newTestRouter()
			r.GET("/files/{path...}/x", echoRoute)
			r.Routes()
		}, "catch-all must be the last segment"},
		{"two parameters side by side", func() {
			r := newTestRouter()
			r.GET("/files/{a}{b}", echoRoute)
			r.Routes()
		}, "side by side"},
		{"a catch-all inside a segment", func() {
			r := newTestRouter()
			r.GET("/files/name-{path...}", echoRoute)
			r.Routes()
		}, "catch-all must span a whole segment"},
		{"two spellings of one segment shape", func() {
			r := newTestRouter()
			r.GET("/reports/rep-{date}.csv", echoRoute)
			r.GET("/reports/rep-{day}.csv", echoRoute)
			r.Routes()
		}, "already uses the parameter names"},
		{"use after route", func() {
			r := newTestRouter()
			r.GET("/a", echoRoute)
			r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] { return next })
		}, "Use must come before"},
		{"route after serving", func() {
			r := newTestRouter()
			r.GET("/a", echoRoute)
			do(r, http.MethodGet, "/a")
			r.GET("/b", echoRoute)
		}, "after the router started serving"},
		{"router mounted inside itself", func() {
			r := newTestRouter()
			r.Mount("/self", r)
			r.Routes()
		}, "mounted inside itself"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				got := recover()
				if got == nil {
					t.Fatalf("no panic, want one that mentions %q", tc.want)
				}
				if msg := fmt.Sprint(got); !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, want one that mentions %q", msg, tc.want)
				}
			}()
			tc.fn()
		})
	}
}

func TestWrapHandler(t *testing.T) {
	r := newTestRouter()
	r.GET("/plain", WrapHandler[*tctx](http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "std")
		})))

	rec := do(r, http.MethodGet, "/plain")
	if got, want := rec.Body.String(), "std"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestRedirectTrailingSlashKeepsTheMethod(t *testing.T) {
	r := newTestRouter()
	r.RedirectTrailingSlash(true)
	r.POST("/users", echoRoute)

	rec := do(r, http.MethodPost, "/users/")
	if rec.Code != http.StatusPermanentRedirect {
		t.Errorf("status = %d, want 308 so that the client keeps the body", rec.Code)
	}
}

func TestConfigurationAfterServingPanics(t *testing.T) {
	r := newTestRouter()
	r.GET("/a", echoRoute)
	do(r, http.MethodGet, "/a")

	defer func() {
		msg := fmt.Sprint(recover())
		if !strings.Contains(msg, "after the router started serving") {
			t.Errorf("panic = %q", msg)
		}
	}()
	r.MaxBodyBytes(1)
}

func TestErrorHandlerCatchesEverything(t *testing.T) {
	type failure struct {
		status int
		msg    string
	}
	var seen []failure

	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) {
		seen = append(seen, failure{StatusOf(err), err.Error()})
		_ = c.String(StatusOf(err), "handled")
	})
	r.GET("/panic", func(*tctx) error { panic("boom") })
	r.GET("/error", func(*tctx) error { return ErrConflict })
	r.POST("/error", echoRoute)

	for _, tc := range []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"a returned error", http.MethodGet, "/error", http.StatusConflict},
		{"a panic", http.MethodGet, "/panic", http.StatusInternalServerError},
		{"no route", http.MethodGet, "/missing", http.StatusNotFound},
		{"the wrong method", http.MethodDelete, "/error", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			rec := do(r, tc.method, tc.path)
			if len(seen) != 1 {
				t.Fatalf("the error handler ran %d times, want 1", len(seen))
			}
			if seen[0].status != tc.status {
				t.Errorf("status = %d, want %d", seen[0].status, tc.status)
			}
			if rec.Body.String() != "handled" {
				t.Errorf("body = %q, want %q", rec.Body.String(), "handled")
			}
		})
	}

	seen = nil
	do(r, http.MethodGet, "/panic")
	if !strings.Contains(seen[0].msg, "panic: boom") {
		t.Errorf("error = %q, want the panic value", seen[0].msg)
	}
	if !strings.Contains(seen[0].msg, "router.(*Router[") && !strings.Contains(seen[0].msg, "goroutine") {
		t.Errorf("error = %q, want a stack", seen[0].msg)
	}
}

func TestPanicInTheErrorHandlerAnswers500(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(func(*tctx, error) { panic("the renderer is broken") })
	r.GET("/", func(*tctx) error { return ErrConflict })

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestErrAbortHandlerStillReachesTheServer(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(*tctx) error { panic(http.ErrAbortHandler) })

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	do(r, http.MethodGet, "/")
}

func TestMountRouterDoesNotShareParameters(t *testing.T) {
	type adminCtx struct {
		Base
	}

	sub := New(func(http.ResponseWriter, *http.Request) *adminCtx { return new(adminCtx) })
	sub.GET("/users/{id}", func(c *adminCtx) error {
		return c.Stringf(http.StatusOK, "tenant=%q id=%q path=%q",
			c.Param("tenant"), c.Param("id"), c.Path())
	})

	r := newTestRouter()
	r.MountRouter("/t/{tenant}", sub)

	rec := do(r, http.MethodGet, "/t/acme/users/7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := `tenant="" id="7" path="/users/7"`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func setHeader(name, value string) Middleware[*tctx] {
	return func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			c.Response().Header().Set(name, value)
			return next(c)
		}
	}
}

func TestMatchedRouteReachesAWrappedHandler(t *testing.T) {
	r := newTestRouter()
	r.GET("/users/{id}/posts/{postID}", WrapHandler[*tctx](http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintf(w, "handler %s id=%s postID=%s",
				req.Pattern, req.PathValue("id"), req.PathValue("postID"))
		})))

	rec := do(r, http.MethodGet, "/users/7/posts/12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := "handler /users/{id}/posts/{postID} id=7 postID=12"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestRequestPatternIsSetForAFallbackToo(t *testing.T) {
	var pattern string
	r := newTestRouter()
	r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			pattern = c.RoutePattern()
			return next(c)
		}
	})
	r.GET("/users/{id}", echoRoute)

	if do(r, http.MethodPost, "/users/7"); pattern != "/users/{id}" {
		t.Errorf("405 labelled %q, want %q", pattern, "/users/{id}")
	}
	pattern = "unset"
	if do(r, http.MethodGet, "/nothing"); pattern != "" {
		t.Errorf("404 labelled %q, want an empty pattern", pattern)
	}
}

func rewritePath(from, to string) Middleware[*tctx] {
	return func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			req := c.Request()
			if rest, ok := strings.CutPrefix(req.URL.Path, from); ok {
				r2 := req.Clone(req.Context())
				r2.URL.Path = to + rest
				c.SetRequest(r2)
			}
			return next(c)
		}
	}
}

func TestPreRewritesThePathBeforeMatching(t *testing.T) {
	r := newTestRouter()
	r.Pre(rewritePath("/old/", "/new/"))
	r.GET("/new/users/{id}", echoRoute)

	rec := do(r, http.MethodGet, "/old/users/7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if want := "/new/users/{id} id=7"; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestPreOverridesTheMethodBeforeMatching(t *testing.T) {
	r := newTestRouter()
	r.Pre(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			if m := c.Request().Header.Get("X-HTTP-Method-Override"); m != "" {
				r2 := c.Request().Clone(c.Request().Context())
				r2.Method = m
				c.SetRequest(r2)
			}
			return next(c)
		}
	})
	r.DELETE("/users/{id}", func(c *tctx) error { return c.String(http.StatusOK, "deleted") })

	req := httptest.NewRequest(http.MethodPost, "/users/7", nil)
	req.Header.Set("X-HTTP-Method-Override", http.MethodDelete)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "deleted" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "deleted")
	}
}

func TestPreRunsBeforeTheRoutingMiddleware(t *testing.T) {
	var order []string
	r := newTestRouter()
	r.Pre(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			order = append(order, "pre pattern="+c.RoutePattern())
			return next(c)
		}
	})
	r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			order = append(order, "use pattern="+c.RoutePattern())
			return next(c)
		}
	})
	r.GET("/users/{id}", func(c *tctx) error {
		order = append(order, "handler pattern="+c.RoutePattern())
		return c.NoContent(http.StatusNoContent)
	})

	do(r, http.MethodGet, "/users/7")
	want := []string{"pre pattern=", "use pattern=/users/{id}", "handler pattern=/users/{id}"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %q, want %q", order, want)
	}
}

func TestPreRunsForAFallbackToo(t *testing.T) {
	ran := 0
	r := newTestRouter()
	r.Pre(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			ran++
			return next(c)
		}
	})
	r.GET("/only", echoRoute)

	if rec := do(r, http.MethodGet, "/missing"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if ran != 1 {
		t.Errorf("the pre stage ran %d times, want 1", ran)
	}
}

func TestPreErrorReachesTheErrorHandler(t *testing.T) {
	r := newTestRouter()
	r.Pre(func(HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(*tctx) error { return ErrForbidden.WithMessage("no entry") }
	})
	r.GET("/", echoRoute)

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no entry") {
		t.Errorf("body = %q, want the message of the error", rec.Body.String())
	}
}

func TestPreDecidesWhatRedirectTrailingSlashSees(t *testing.T) {
	r := newTestRouter()
	r.RedirectTrailingSlash(true)
	r.Pre(rewritePath("/old/", "/new/"))
	r.GET("/new/users", echoRoute)

	rec := do(r, http.MethodGet, "/old/users/")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/new/users" {
		t.Errorf("Location = %q, want %q, the rewritten path", got, "/new/users")
	}
}

func TestPreRunsBeforeTheHostMatch(t *testing.T) {
	r := newTestRouter()
	r.Pre(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			r2 := c.Request().Clone(c.Request().Context())
			r2.Host = "api.example.com"
			c.SetRequest(r2)
			return next(c)
		}
	})
	r.Host("api.example.com", func(h *Router[*tctx]) {
		h.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "api") })
	})
	r.Host("example.com", func(h *Router[*tctx]) {
		h.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "site") })
	})

	if got := doHost(r, http.MethodGet, "example.com", "/").Body.String(); got != "api" {
		t.Errorf("body = %q, want %q, so the host match reads the rewritten request", got, "api")
	}
}

func TestPrefixScopeAnswersItsOwnFallbacks(t *testing.T) {
	r := newTestRouter()
	r.Route("/api", func(g *Router[*tctx]) {
		g.Use(setHeader("X-Scope", "api"))
		g.GET("/users", echoRoute)
	})
	r.GET("/health", echoRoute)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantScope  string
	}{
		{"a 404 under the prefix", http.MethodGet, "/api/typo", http.StatusNotFound, "api"},
		{"a 405 under the prefix", http.MethodPost, "/api/users", http.StatusMethodNotAllowed, "api"},
		{"an OPTIONS under the prefix", http.MethodOptions, "/api/users", http.StatusNoContent, "api"},
		{"the prefix itself", http.MethodGet, "/api", http.StatusNotFound, "api"},
		{"a 404 outside the prefix", http.MethodGet, "/typo", http.StatusNotFound, ""},
		{"a 405 outside the prefix", http.MethodPost, "/health", http.StatusMethodNotAllowed, ""},
		{"a path that only starts like the prefix", http.MethodGet, "/apifoo", http.StatusNotFound, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, tc.method, tc.path)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("X-Scope"); got != tc.wantScope {
				t.Errorf("X-Scope = %q, want %q", got, tc.wantScope)
			}
		})
	}
}

func TestPrefixScopeFallbackPicksTheInnermostScope(t *testing.T) {
	r := newTestRouter()
	r.Use(setHeader("X-Root", "root"))
	r.Route("/api", func(g *Router[*tctx]) {
		g.Use(setHeader("X-Api", "api"))
		g.GET("/ping", echoRoute)
		g.Route("/v1", func(v *Router[*tctx]) {
			v.Use(setHeader("X-V1", "v1"))
			v.GET("/users", echoRoute)
		})
	})

	tests := []struct {
		path string
		want [3]string
	}{
		{"/api/v1/typo", [3]string{"root", "api", "v1"}},
		{"/api/typo", [3]string{"root", "api", ""}},
		{"/typo", [3]string{"root", "", ""}},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := do(r, http.MethodGet, tc.path)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			got := [3]string{
				rec.Header().Get("X-Root"), rec.Header().Get("X-Api"), rec.Header().Get("X-V1"),
			}
			if got != tc.want {
				t.Errorf("headers = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrefixScopeFallbackCoversAParameterPrefix(t *testing.T) {
	r := newTestRouter()
	r.Route("/t/{tenant}", func(g *Router[*tctx]) {
		g.Use(setHeader("X-Scope", "tenant"))
		g.GET("/dashboard", echoRoute)
	})

	if got := do(r, http.MethodGet, "/t/acme/typo").Header().Get("X-Scope"); got != "tenant" {
		t.Errorf("X-Scope = %q, want %q", got, "tenant")
	}
	if got := do(r, http.MethodGet, "/other").Header().Get("X-Scope"); got != "" {
		t.Errorf("X-Scope = %q, want none", got)
	}
}

func TestMountedRoutesKeepTheFallbackOfTheirPrefix(t *testing.T) {
	sub := newTestRouter()
	sub.GET("/users", echoRoute)

	r := newTestRouter()
	r.Route("/api", func(g *Router[*tctx]) {
		g.Use(setHeader("X-Scope", "api"))
		g.Mount("/v1", sub)
	})

	if got := do(r, http.MethodGet, "/api/v1/typo").Header().Get("X-Scope"); got != "api" {
		t.Errorf("X-Scope = %q, want %q", got, "api")
	}
}

func TestRoutesReportsTheMetadata(t *testing.T) {
	type op struct{ Summary string }

	r := newTestRouter()
	r.Meta(op{Summary: "read a user"}).GET("/users/{id}", echoRoute)
	r.Meta(op{Summary: "create a user"}).POST("/users", echoRoute)
	r.GET("/health", echoRoute)

	got := r.Routes()
	want := []Route{
		{Method: http.MethodGet, Pattern: "/health"},
		{Method: http.MethodPost, Pattern: "/users", Meta: op{Summary: "create a user"}},
		{Method: http.MethodGet, Pattern: "/users/{id}", Meta: op{Summary: "read a user"}, Params: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d routes, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("route %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMetaScopeCarriesEveryRouteOnIt(t *testing.T) {
	g := newTestRouter()
	tagged := g.Meta("shared")
	tagged.GET("/a", echoRoute)
	tagged.POST("/b", echoRoute)

	for _, rt := range g.Routes() {
		if rt.Meta != "shared" {
			t.Errorf("route %s %s carries %v, want %q", rt.Method, rt.Pattern, rt.Meta, "shared")
		}
	}
}

func TestAnyAnswersAMethodOutsideTheStandardSet(t *testing.T) {
	r := newTestRouter()
	r.Any("/rpc", func(c *tctx) error { return c.String(http.StatusOK, "any "+c.Request().Method) })

	for _, method := range []string{http.MethodGet, http.MethodPost, MethodQuery, "PROPFIND", "MKCOL"} {
		rec := do(r, method, "/rpc")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", method, rec.Code)
		}
		if want := "any " + method; rec.Body.String() != want {
			t.Errorf("%s: body = %q, want %q", method, rec.Body.String(), want)
		}
	}
}

func TestExplicitMethodBeatsAny(t *testing.T) {
	tests := []struct {
		name     string
		register func(*Router[*tctx])
	}{
		{"the explicit route first", func(r *Router[*tctx]) {
			r.POST("/x", func(c *tctx) error { return c.String(http.StatusOK, "post") })
			r.Any("/x", func(c *tctx) error { return c.String(http.StatusOK, "any") })
		}},
		{"the any route first", func(r *Router[*tctx]) {
			r.Any("/x", func(c *tctx) error { return c.String(http.StatusOK, "any") })
			r.POST("/x", func(c *tctx) error { return c.String(http.StatusOK, "post") })
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			tc.register(r)

			if got := do(r, http.MethodPost, "/x").Body.String(); got != "post" {
				t.Errorf("POST = %q, want %q", got, "post")
			}
			if got := do(r, http.MethodPut, "/x").Body.String(); got != "any" {
				t.Errorf("PUT = %q, want %q", got, "any")
			}
			if rec := do(r, "PROPFIND", "/x"); rec.Code != http.StatusOK || rec.Header().Get(HeaderAllow) != "" {
				t.Errorf("PROPFIND: status = %d, Allow = %q, want 200 and no Allow header", rec.Code, rec.Header().Get(HeaderAllow))
			}
		})
	}
}

func TestAnyServesHeadFromTheGetRouteFirst(t *testing.T) {
	ran := ""
	r := newTestRouter()
	r.GET("/x", func(c *tctx) error { ran = "get"; return c.String(http.StatusOK, "get") })
	r.Any("/x", func(c *tctx) error { ran = "any"; return c.String(http.StatusOK, "any") })

	rec := do(r, http.MethodHead, "/x")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ran != "get" {
		t.Errorf("the %s handler answered the HEAD, want the get one", ran)
	}
}

func TestAnyRegistersOneRoute(t *testing.T) {
	r := newTestRouter()
	r.Any("/rpc", echoRoute)

	got := r.Routes()
	want := []Route{{Method: "*", Pattern: "/rpc"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Routes() = %v, want %v", got, want)
	}
}

func TestMountHandlerForwardsANonStandardMethod(t *testing.T) {
	mounted := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprintf(w, "%s %s", req.Method, req.URL.Path)
	})

	r := newTestRouter()
	r.MountHandler("/dav", mounted)

	tests := []struct{ method, target, want string }{
		{"PROPFIND", "/dav/docs/report.txt", "PROPFIND /docs/report.txt"},
		{"MKCOL", "/dav/docs", "MKCOL /docs"},
		{MethodQuery, "/dav", "QUERY /"},
		{http.MethodGet, "/dav/a/b", "GET /a/b"},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rec := do(r, tc.method, tc.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBindReadsTheQueryOfAQueryRequest(t *testing.T) {
	type filter struct {
		Term string `query:"term"`
	}

	r := newTestRouter()
	r.Any("/search", func(c *tctx) error {
		in, err := c.Bind[filter]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, in.Term)
	})

	req := httptest.NewRequest(MethodQuery, "/search?term=books", strings.NewReader("term=records"))
	req.Header.Set(HeaderContentType, MIMEApplicationForm)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "books" {
		t.Errorf("body = %q, want %q, the query string", got, "books")
	}
}

func TestRegistrationPanicsOnABadTable(t *testing.T) {
	tests := []struct {
		name  string
		build func(*Router[*tctx])
		want  string
	}{
		{"a duplicate route", func(r *Router[*tctx]) {
			r.GET("/a", echoRoute)
			r.GET("/a", echoRoute)
		}, "already registered"},
		{"a malformed pattern", func(r *Router[*tctx]) {
			r.GET("/files/{path...}/x", echoRoute)
		}, "catch-all must be the last segment"},
		{"an unbalanced brace", func(r *Router[*tctx]) {
			r.GET("/users/{id", echoRoute)
		}, "unbalanced"},
		{"conflicting parameter names", func(r *Router[*tctx]) {
			r.GET("/users/{id}", echoRoute)
			r.GET("/users/{uid}/posts", echoRoute)
		}, "already named"},
		{"a router mounted inside itself", func(r *Router[*tctx]) {
			r.Mount("/self", r)
		}, "mounted inside itself"},
		{"a route on a mounted subrouter", func(r *Router[*tctx]) {
			sub := newTestRouter()
			sub.GET("/a", echoRoute)
			r.Mount("/sub", sub)
			sub.GET("/b", echoRoute)
		}, "on a mounted router"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mustPanicContaining(t, tc.want, func() { tc.build(newTestRouter()) })
		})
	}
}

func TestAValidTableRegistersWithoutPanicking(t *testing.T) {
	r := newTestRouter()
	r.GET("/users/{id}", echoRoute)
	r.POST("/users", echoRoute)
	if rec := do(r, http.MethodGet, "/users/7"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestTheFirstRequestClosesRegistration(t *testing.T) {
	r := newTestRouter()
	r.GET("/a", echoRoute)
	if rec := do(r, http.MethodGet, "/a"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	mustPanicContaining(t, "after the router started serving", func() { r.GET("/b", echoRoute) })
}

type record struct {
	route  string
	status int
	size   int64
	err    string
}

func observedRouter(t *testing.T) (*Router[*tctx], *[]record) {
	t.Helper()
	var got []record
	r := newTestRouter()
	r.Observe(func(c Context, status int, size int64, _ time.Duration, err error) {
		e := ""
		if err != nil {
			e = err.Error()
		}
		got = append(got, record{route: c.RoutePattern(), status: status, size: size, err: e})
	})
	return r, &got
}

func TestObserveReportsEveryRequest(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantRoute  string
		wantStatus int
		wantErr    string
	}{
		{"a handler that answered", http.MethodGet, "/users/7", "/users/{id}", http.StatusOK, ""},
		{"a handler that failed", http.MethodGet, "/boom", "/boom", http.StatusConflict, "409 Conflict"},
		{"a handler that panicked", http.MethodGet, "/panic", "/panic", http.StatusInternalServerError, "the handler broke"},
		{"a path with no route", http.MethodGet, "/missing", "", http.StatusNotFound, "404 Not Found"},
		{"a method that no route answers", http.MethodPost, "/users/7", "/users/{id}", http.StatusMethodNotAllowed, "405 Method Not Allowed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, got := observedRouter(t)
			r.GET("/users/{id}", func(c *tctx) error { return c.String(http.StatusOK, "ok") })
			r.GET("/boom", func(*tctx) error { return ErrConflict })
			r.GET("/panic", func(*tctx) error { panic("the handler broke") })

			rec := do(r, tc.method, tc.path)
			if len(*got) != 1 {
				t.Fatalf("the observer ran %d times, want 1", len(*got))
			}
			g := (*got)[0]
			if g.route != tc.wantRoute {
				t.Errorf("route = %q, want %q", g.route, tc.wantRoute)
			}
			if g.status != tc.wantStatus || g.status != rec.Code {
				t.Errorf("status = %d, want %d and the %d that the client saw", g.status, tc.wantStatus, rec.Code)
			}
			if g.size != int64(rec.Body.Len()) {
				t.Errorf("size = %d, want %d, the bytes of the body", g.size, rec.Body.Len())
			}
			if tc.wantErr == "" && g.err != "" {
				t.Errorf("error = %q, want none", g.err)
			}
			if tc.wantErr != "" && !strings.Contains(g.err, tc.wantErr) {
				t.Errorf("error = %q, want one that mentions %q", g.err, tc.wantErr)
			}
		})
	}
}

func TestObserveReportsTheStatusTheClientSaw(t *testing.T) {
	r, got := observedRouter(t)
	r.GET("/late", func(c *tctx) error {
		//nolint:errcheck // the write is the point of the test
		c.String(http.StatusCreated, "written")
		return ErrConflict
	})

	do(r, http.MethodGet, "/late")
	if len(*got) != 1 {
		t.Fatalf("the observer ran %d times, want 1", len(*got))
	}
	if (*got)[0].status != http.StatusCreated {
		t.Errorf("status = %d, want 201, the one the client saw", (*got)[0].status)
	}
	if (*got)[0].err == "" {
		t.Error("the observer reports no error, want the one the handler returned")
	}
}

func TestObserveMeasuresTheRequest(t *testing.T) {
	var d time.Duration
	r := newTestRouter()
	r.Observe(func(_ Context, _ int, _ int64, took time.Duration, _ error) { d = took })
	r.GET("/", func(c *tctx) error { return c.NoContent(http.StatusNoContent) })

	do(r, http.MethodGet, "/")
	if d <= 0 {
		t.Errorf("duration = %v, want a positive one", d)
	}
}

func TestObservePoolsTheContextAfterItReads(t *testing.T) {
	var got []string
	r := NewPooled(func() *tctx { return new(tctx) }, func(c *tctx) { c.Tag = "" })
	r.Observe(func(c Context, _ int, _ int64, _ time.Duration, _ error) {
		got = append(got, c.RoutePattern())
	})
	r.GET("/users/{id}", func(c *tctx) error { return c.NoContent(http.StatusNoContent) })

	do(r, http.MethodGet, "/users/7")
	do(r, http.MethodGet, "/users/8")
	if want := []string{"/users/{id}", "/users/{id}"}; !slices.Equal(got, want) {
		t.Errorf("the observer saw %q, want %q", got, want)
	}
}

func TestObserveLetsErrAbortHandlerThrough(t *testing.T) {
	ran := false
	r := newTestRouter()
	r.Observe(func(Context, int, int64, time.Duration, error) { ran = true })
	r.GET("/", func(*tctx) error { panic(http.ErrAbortHandler) })

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
		if ran {
			t.Error("the observer ran; an aborted request asks for no record")
		}
	}()
	do(r, http.MethodGet, "/")
}

func TestRegistrationPanics(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
		want string
	}{
		{"Pre on a group", func() {
			r := newTestRouter()
			r.Group(func(g *Router[*tctx]) { g.Pre(setHeader("X", "1")) })
		}, "Pre belongs to the root router"},
		{"Pre on a prefix scope", func() {
			r := newTestRouter()
			r.Route("/api", func(g *Router[*tctx]) { g.Pre(setHeader("X", "1")) })
		}, "Pre belongs to the root router"},
		{"Pre on a host scope", func() {
			r := newTestRouter()
			r.Host("example.com", func(h *Router[*tctx]) { h.Pre(setHeader("X", "1")) })
		}, "Pre belongs to the root router"},
		{"Pre after serving", func() {
			r := newTestRouter()
			r.GET("/", echoRoute)
			do(r, http.MethodGet, "/")
			r.Pre(setHeader("X", "1"))
		}, "after the router started serving"},
		{"Match with no methods", func() {
			newTestRouter().Match(nil, "/x", echoRoute)
		}, "Match needs at least one method"},
		{"Match with an empty method", func() {
			newTestRouter().Match([]string{""}, "/x", echoRoute)
		}, "Match needs non-empty methods"},
		{"Observe after serving", func() {
			r := newTestRouter()
			r.GET("/", echoRoute)
			do(r, http.MethodGet, "/")
			r.Observe(func(Context, int, int64, time.Duration, error) {})
		}, "after the router started serving"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				got := recover()
				if got == nil {
					t.Fatalf("no panic, want one that mentions %q", tc.want)
				}
				if msg := fmt.Sprint(got); !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, want one that mentions %q", msg, tc.want)
				}
			}()
			tc.fn()
		})
	}
}

func TestObserveReportsARequestThatThePreStageAnswered(t *testing.T) {
	r, got := observedRouter(t)
	r.Pre(rewritePath("/old/", "/new/"))
	r.Pre(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			if c.Request().URL.Path == "/new/blocked" {
				return ErrForbidden
			}
			return next(c)
		}
	})
	r.GET("/new/users/{id}", func(c *tctx) error { return c.String(http.StatusOK, "ok") })

	do(r, http.MethodGet, "/old/users/7")
	do(r, http.MethodGet, "/old/blocked")

	if len(*got) != 2 {
		t.Fatalf("the observer ran %d times, want 2", len(*got))
	}
	if g := (*got)[0]; g.route != "/new/users/{id}" || g.status != http.StatusOK || g.err != "" {
		t.Errorf("the rewritten request = %+v, want the matched route and a 200", g)
	}
	if g := (*got)[1]; g.route != "" || g.status != http.StatusForbidden || g.err == "" {
		t.Errorf("the blocked request = %+v, want no route, a 403 and the error", g)
	}
}

func TestASingleScopeFallbackLeavesTheRestOfTheRouterAlone(t *testing.T) {
	r := newTestRouter()
	r.Route("/api", func(g *Router[*tctx]) {
		g.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "api 404") })
		g.GET("/users", echoRoute)
	})
	r.GET("/health", echoRoute)

	if got := do(r, http.MethodGet, "/api/typo").Body.String(); got != "api 404" {
		t.Errorf("GET /api/typo = %q, want %q", got, "api 404")
	}
	if got := do(r, http.MethodGet, "/typo").Body.String(); got == "api 404" {
		t.Errorf("GET /typo = %q, want the fallback of the root", got)
	}
}

func TestAScopeFallbackReachesTheScopesBelowIt(t *testing.T) {
	r := newTestRouter()
	r.Route("/api", func(g *Router[*tctx]) {
		g.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "api 404") })
		g.Route("/v1", func(v *Router[*tctx]) { v.GET("/users", echoRoute) })
	})

	if got := do(r, http.MethodGet, "/api/v1/typo").Body.String(); got != "api 404" {
		t.Errorf("GET /api/v1/typo = %q, want %q", got, "api 404")
	}
}

func TestScopeErrorHandlerAnswersItsScopeAlone(t *testing.T) {
	r := newTestRouter()
	r.Route("/debug", func(g *Router[*tctx]) {
		g.ErrorHandler(ErrorHandler[*tctx](true))
		g.GET("/dump", func(*tctx) error {
			return ErrInternalServerError.WithError(errors.New("debug cause"))
		})
	})
	r.GET("/checkout", func(*tctx) error {
		return ErrInternalServerError.WithError(errors.New("billing password"))
	})

	if got := do(r, http.MethodGet, "/debug/dump").Body.String(); !strings.Contains(got, "debug cause") {
		t.Errorf("GET /debug/dump = %q, want the cause that the scope exposes", got)
	}
	if got := do(r, http.MethodGet, "/checkout").Body.String(); strings.Contains(got, "billing password") {
		t.Errorf("GET /checkout = %q, want no internal cause outside the scope that exposes one", got)
	}
}

func TestRedirectTrailingSlashNeverPointsAtAnotherHost(t *testing.T) {
	r := newTestRouter()
	r.RedirectTrailingSlash(true)
	r.GET("/{path...}", echoRoute)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			for _, target := range []string{"//evil.com/", "///evil.com/"} {
				rec := do(r, method, target)
				loc := rec.Header().Get("Location")
				if strings.HasPrefix(loc, "//") {
					t.Errorf("%s %q: Location = %q, which points at another origin", method, target, loc)
				}
			}
		})
	}
}

func TestAllowHeaderNamesTheMethodsOfEveryMatchingRoute(t *testing.T) {
	tests := []struct {
		name     string
		register func(r *Router[*tctx])
		path     string
	}{
		{
			name: "a catch-all under a literal",
			register: func(r *Router[*tctx]) {
				r.GET("/files/{path...}", echoRoute)
				r.POST("/files/a", echoRoute)
			},
			path: "/files/a",
		},
		{
			name: "a parameter under a literal",
			register: func(r *Router[*tctx]) {
				r.GET("/a/{id}", echoRoute)
				r.POST("/a/b", echoRoute)
			},
			path: "/a/b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			tc.register(r)

			rec := do(r, http.MethodDelete, tc.path)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", rec.Code)
			}
			allow := rec.Header().Get(HeaderAllow)
			named := strings.Split(allow, ", ")
			if want := []string{"GET", "HEAD", "OPTIONS", "POST"}; !slices.Equal(named, want) {
				t.Fatalf("Allow = %q, want %q", allow, strings.Join(want, ", "))
			}
			for _, m := range named {
				if got := do(r, m, tc.path).Code; got == http.StatusMethodNotAllowed {
					t.Errorf("Allow names %s, but the router answers 405 to it", m)
				}
			}
		})
	}
}

func TestAMountedRouterKeepsTheFallbacksOfTheRoot(t *testing.T) {
	sub := newTestRouter()
	sub.GET("/users", echoRoute)

	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "root: "+err.Error()) })
	r.Mount("/api", sub)
	r.GET("/boom", func(*tctx) error { return errors.New("kettle") })

	for _, path := range []string{"/api/typo", "/typo"} {
		rec := do(r, http.MethodGet, path)
		if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Body.String(), "root: ") {
			t.Errorf("GET %s = %d %q, want a 404 from the root error handler", path, rec.Code, rec.Body.String())
		}
	}
	rec := do(r, http.MethodGet, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /boom: status = %d, want %d", rec.Code, http.StatusTeapot)
	}
}

func TestAGroupInsideAPrefixOwnsTheFallbackOfThatPrefix(t *testing.T) {
	r := newTestRouter()
	r.Route("/api", func(g *Router[*tctx]) {
		g.Group(func(h *Router[*tctx]) {
			h.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "api 404") })
			h.GET("/users", echoRoute)
		})
	})
	r.GET("/health", echoRoute)

	if got := do(r, http.MethodGet, "/api/typo").Body.String(); got != "api 404" {
		t.Errorf("GET /api/typo = %q, want %q", got, "api 404")
	}
	if got := do(r, http.MethodGet, "/typo").Body.String(); got == "api 404" {
		t.Errorf("GET /typo = %q, want the fallback of the root", got)
	}
}

func TestAScopeErrorHandlerInsideAHostAnswersThatHostAlone(t *testing.T) {
	boom := func(*tctx) error { return errors.New("boom") }

	r := newTestRouter()
	r.Host("api.example.com", func(h *Router[*tctx]) {
		h.Route("/debug", func(g *Router[*tctx]) {
			g.ErrorHandler(func(c *tctx, err error) { _ = c.String(http.StatusTeapot, "debug") })
			g.GET("/dump", boom)
		})
		h.GET("/other", boom)
	})
	r.Host("www.example.com", func(h *Router[*tctx]) {
		h.GET("/debug/dump", boom)
	})

	tests := []struct {
		host string
		path string
		want int
	}{
		{"api.example.com", "/debug/dump", http.StatusTeapot},
		{"api.example.com", "/other", http.StatusInternalServerError},
		{"www.example.com", "/debug/dump", http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+tc.path, nil)
			r.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestUseAfterATaggedRoutePanics(t *testing.T) {
	register := map[string]func(*Router[*tctx]){
		"Meta":  func(r *Router[*tctx]) { r.Meta("op").GET("/u", echoRoute) },
		"With":  func(r *Router[*tctx]) { r.With().GET("/u", echoRoute) },
		"plain": func(r *Router[*tctx]) { r.GET("/u", echoRoute) },
		"Group": func(r *Router[*tctx]) { r.Group(func(g *Router[*tctx]) { g.GET("/u", echoRoute) }) },
		"Route": func(r *Router[*tctx]) { r.Route("/g", func(g *Router[*tctx]) { g.GET("/u", echoRoute) }) },
	}
	for name, add := range register {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("Use after a route that %s registered did not panic", name)
				}
			}()
			r := newTestRouter()
			add(r)
			r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] { return next })
		})
	}
}

func TestUseBeforeAGroupIsAllowed(t *testing.T) {
	r := newTestRouter()
	r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] { return next })
	r.Group(func(g *Router[*tctx]) { g.GET("/a", echoRoute) })
	if rec := do(r, http.MethodGet, "/a"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestPreDispatchesEachErrorOnce(t *testing.T) {
	r := newTestRouter()
	r.Pre(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] { return next })
	calls := 0
	r.ErrorHandler(func(c *tctx, err error) {
		calls++
		_ = c.NoContent(StatusOf(err))
	})
	r.GET("/fail", func(*tctx) error { return ErrConflict })

	for _, path := range []string{"/fail", "/missing"} {
		before := calls
		do(r, http.MethodGet, path)
		if calls != before+1 {
			t.Errorf("GET %s invoked the error handler %d times, want 1", path, calls-before)
		}
	}
}

func TestErrorScopeIsSelectedByRouting(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "root") })
	r.Pre(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			if c.Path() == "/debug/pre" {
				return ErrConflict
			}
			return next(c)
		}
	})
	r.Route("/debug", func(g *Router[*tctx]) {
		g.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "debug") })
		g.GET("/mutate", func(c *tctx) error {
			req := c.Request().Clone(c.Request().Context())
			req.URL.Path = "/outside"
			req.URL.RawPath = ""
			c.SetRequest(req)
			return ErrConflict
		})
	})

	if got := do(r, http.MethodGet, "/debug/pre").Body.String(); got != "root" {
		t.Errorf("pre-routing error used %q, want root handler", got)
	}
	if got := do(r, http.MethodGet, "/debug/mutate").Body.String(); got != "debug" {
		t.Errorf("routed error after URL mutation used %q, want debug handler", got)
	}
}

func TestScopedFallbackUsesParsedPrefix(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			r := newTestRouter()
			addPlain := func() {
				r.Route("/t/{value}", func(g *Router[*tctx]) {
					g.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "plain") })
				})
			}
			addNumeric := func() {
				r.Route("/t/{value:[0-9]+}", func(g *Router[*tctx]) {
					g.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "numeric") })
				})
			}
			if reverse {
				addNumeric()
				addPlain()
			} else {
				addPlain()
				addNumeric()
			}

			if got := do(r, http.MethodGet, "/t/42/missing").Body.String(); got != "numeric" {
				t.Errorf("numeric prefix chose %q", got)
			}
			if got := do(r, http.MethodGet, "/t/nope/missing").Body.String(); got != "plain" {
				t.Errorf("plain prefix chose %q", got)
			}
		})
	}

	r := newTestRouter()
	r.Route("/reports/report-{id:[0-9]+}.csv", func(g *Router[*tctx]) {
		g.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "report") })
	})
	if got := do(r, http.MethodGet, "/reports/report-7.csv/missing").Body.String(); got != "report" {
		t.Errorf("template scope chose %q", got)
	}
	if got := do(r, http.MethodGet, "/reports/report-no.csv/missing").Body.String(); got == "report" {
		t.Error("template scope covered a value rejected by its regex")
	}
}

func TestScopedFallbackPrecedenceIsLexicographic(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			r := newTestRouter()
			register := func(prefix, name string) {
				r.Route(prefix, func(g *Router[*tctx]) {
					g.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
						return func(c *tctx) error {
							c.SetHeader("X-Scope", name)
							return next(c)
						}
					})
					g.ErrorHandler(func(c *tctx, err error) {
						_ = c.String(StatusOf(err), name+" "+strconv.Itoa(StatusOf(err)))
					})
				})
			}
			if reverse {
				register("/{x}/b", "parameter-first")
				register("/a/{x}", "static-first")
			} else {
				register("/a/{x}", "static-first")
				register("/{x}/b", "parameter-first")
			}
			r.GET("/a/b/method", echoRoute)
			r.GET("/a/b/error", func(*tctx) error { return errors.New("boom") })

			if rec := do(r, http.MethodGet, "/a/b/missing"); rec.Code != http.StatusNotFound || rec.Body.String() != "static-first 404" {
				t.Errorf("404 = %d %q", rec.Code, rec.Body.String())
			}
			if rec := do(r, http.MethodPost, "/a/b/method"); rec.Code != http.StatusMethodNotAllowed || rec.Body.String() != "static-first 405" {
				t.Errorf("405 = %d %q", rec.Code, rec.Body.String())
			}
			if rec := do(r, http.MethodOptions, "/a/b/method"); rec.Code != http.StatusNoContent || rec.Header().Get("X-Scope") != "static-first" {
				t.Errorf("OPTIONS = %d scope %q", rec.Code, rec.Header().Get("X-Scope"))
			}
			if rec := do(r, http.MethodGet, "/a/b/error"); rec.Code != http.StatusInternalServerError || rec.Body.String() != "static-first 500" {
				t.Errorf("error = %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func registerScopedHandlers(r *Router[*tctx], prefix, name string) {
	r.Route(prefix, func(g *Router[*tctx]) {
		g.ErrorHandler(func(c *tctx, err error) {
			_ = c.String(StatusOf(err), name+" "+strconv.Itoa(StatusOf(err)))
		})
		g.GET("/method", func(c *tctx) error { return c.NoContent(http.StatusNoContent) })
		g.GET("/error", func(*tctx) error { return errors.New("boom") })
	})
}

func TestEscapedStaticScopeKeepsItsFallbacksAndErrorHandler(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			r := newTestRouter()
			static := func() { registerScopedHandlers(r, "/x/a%2Fb", "static") }
			dynamic := func() { registerScopedHandlers(r, "/x/{value}", "dynamic") }
			if reverse {
				dynamic()
				static()
			} else {
				static()
				dynamic()
			}

			tests := []struct {
				method string
				path   string
				code   int
				body   string
			}{
				{http.MethodGet, "/x/a%2Fb/missing", http.StatusNotFound, "static 404"},
				{http.MethodGet, "/x/a%2fb/missing", http.StatusNotFound, "static 404"},
				{http.MethodPost, "/x/a%2Fb/method", http.StatusMethodNotAllowed, "static 405"},
				{http.MethodPost, "/x/a%2fb/method", http.StatusMethodNotAllowed, "static 405"},
				{http.MethodGet, "/x/a%2Fb/error", http.StatusInternalServerError, "static 500"},
				{http.MethodGet, "/x/a%2fb/error", http.StatusInternalServerError, "static 500"},
			}
			for _, tc := range tests {
				rec := do(r, tc.method, tc.path)
				if rec.Code != tc.code || rec.Body.String() != tc.body {
					t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.path, rec.Code, rec.Body.String(), tc.code, tc.body)
				}
			}
		})
	}
}

func TestScopedHandlersMatchCanonicalAndDynamicEscapes(t *testing.T) {
	t.Run("unreserved static", func(t *testing.T) {
		r := newTestRouter()
		registerScopedHandlers(r, "/x/alpha", "static")
		if got := do(r, http.MethodGet, "/x/%61lpha/missing").Body.String(); got != "static 404" {
			t.Errorf("fallback = %q, want %q", got, "static 404")
		}
		if got := do(r, http.MethodGet, "/x/%61lpha/error").Body.String(); got != "static 500" {
			t.Errorf("error = %q, want %q", got, "static 500")
		}
	})

	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("template and regex reverse=%v", reverse), func(t *testing.T) {
			r := newTestRouter()
			template := func() { registerScopedHandlers(r, "/x/pre-{value}-post", "template") }
			regex := func() { registerScopedHandlers(r, "/x/{value:.+}", "regex") }
			if reverse {
				regex()
				template()
			} else {
				template()
				regex()
			}
			if got := do(r, http.MethodGet, "/x/pre-a%2Fb-post/missing").Body.String(); got != "template 404" {
				t.Errorf("fallback = %q, want %q", got, "template 404")
			}
			if got := do(r, http.MethodGet, "/x/pre-a%2Fb-post/error").Body.String(); got != "template 500" {
				t.Errorf("error = %q, want %q", got, "template 500")
			}
		})
	}

	r := newTestRouter()
	registerScopedHandlers(r, "/x/{value:.+}", "regex")
	if got := do(r, http.MethodGet, "/x/a%2Fb/missing").Body.String(); got != "regex 404" {
		t.Errorf("regex fallback = %q, want %q", got, "regex 404")
	}
	if got := do(r, http.MethodGet, "/x/a%2Fb/error").Body.String(); got != "regex 500" {
		t.Errorf("regex error = %q, want %q", got, "regex 500")
	}
}

func TestScopeCoverageRejectsInvalidDynamicEscapes(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"/x/%zz", "/x/%zz/missing", true},
		{"/x/{value}", "/x/%zz/missing", false},
		{"/x/pre-{value}", "/x/pre-%zz/missing", false},
		{"/x/{value:.+}", "/x/%zz/missing", false},
		{"/x/{rest...}", "/x", true},
		{"/x/y", "/x", false},
	}
	for _, tc := range tests {
		segs, _, err := parsePattern(tc.pattern)
		if err != nil {
			t.Fatalf("parsePattern(%q) = %v", tc.pattern, err)
		}
		scope := scopeFallback[*tctx]{pattern: segs}
		if got := scope.covers(tc.path, true); got != tc.want {
			t.Errorf("%q covers %q = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestScopedHandlersIgnoreInvalidRawPath(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) {
		_ = c.String(StatusOf(err), "root "+strconv.Itoa(StatusOf(err)))
	})
	registerScopedHandlers(r, "/x/safe", "scope")
	r.GET("/outside/error", func(*tctx) error { return errors.New("boom") })

	tests := []struct {
		path string
		body string
	}{
		{"/outside/missing", "root 404"},
		{"/outside/error", "root 500"},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.URL.RawPath = "/x/safe/%zz"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if got := rec.Body.String(); got != tc.body {
			t.Errorf("GET %s with invalid RawPath = %q, want %q", tc.path, got, tc.body)
		}
	}
}

func TestAScopeValidatesItsPrefix(t *testing.T) {
	r := newTestRouter()
	mustPanicContaining(t, "unbalanced", func() {
		r.Route("/bad/{", func(g *Router[*tctx]) {
			g.ErrorHandler(func(c *tctx, err error) { _ = c.NoContent(StatusOf(err)) })
		})
	})
}

func TestEscapedSegmentsAreDecodedOnceForMatching(t *testing.T) {
	r := newTestRouter()
	r.GET("/digits/1", func(c *tctx) error { return c.String(http.StatusOK, "static") })
	r.GET("/digits/{id:[0-9]+}", func(c *tctx) error { return c.String(http.StatusOK, "numeric "+c.Param("id")) })
	r.GET("/files/{name:[a-z]+}", func(c *tctx) error { return c.String(http.StatusOK, "safe "+c.Param("name")) })
	r.GET("/files/{name}", func(c *tctx) error { return c.String(http.StatusOK, "plain "+c.Param("name")) })
	r.GET("/encoded/{value:.*}", func(c *tctx) error { return c.String(http.StatusOK, c.Param("value")) })
	r.GET("/report/report-{id:[0-9]+}.csv", func(c *tctx) error { return c.String(http.StatusOK, c.Param("id")) })

	tests := []struct{ path, want string }{
		{"/digits/%31", "static"},
		{"/files/a%2Fb", "plain a/b"},
		{"/encoded/%252F", "%2F"},
		{"/report/report-%31.csv", "1"},
	}
	for _, tc := range tests {
		if got := do(r, http.MethodGet, tc.path).Body.String(); got != tc.want {
			t.Errorf("GET %s = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestInconsistentRawPathIsIgnored(t *testing.T) {
	r := newTestRouter()
	r.GET("/admin", func(c *tctx) error { return c.String(http.StatusOK, "admin") })
	r.GET("/public", func(c *tctx) error { return c.String(http.StatusOK, "public") })

	for _, raw := range []string{"/public", "/%zz"} {
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		req.URL.RawPath = raw
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if got := rec.Body.String(); got != "admin" {
			t.Errorf("RawPath %q routed to %q, want Path /admin", raw, got)
		}
	}
}

func TestMountedRouterFreezesWithParent(t *testing.T) {
	sub := newTestRouter()
	sub.GET("/ready", echoRoute)
	first := newTestRouter()
	second := newTestRouter()
	first.Mount("/one", sub)
	second.Mount("/two", sub)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("mounted router accepted a route after it was mounted")
			}
		}()
		sub.GET("/late", echoRoute)
	}()
	if got := do(second, http.MethodGet, "/two/ready").Code; got != http.StatusOK {
		t.Errorf("second parent status = %d, want 200", got)
	}
	if got := do(first, http.MethodGet, "/one/ready").Code; got != http.StatusOK {
		t.Errorf("first parent status = %d, want 200", got)
	}

	mustPanicContaining(t, "after the router started serving", func() {
		first.Mount("/late", newTestRouter())
	})
}

func TestParentsSharingAMountBuildConcurrently(t *testing.T) {
	sub := newTestRouter()
	sub.GET("/ready", echoRoute)
	left, right := newTestRouter(), newTestRouter()
	left.Mount("/left", sub)
	right.Mount("/right", sub)

	start := make(chan struct{})
	done := make(chan int, 2)
	for _, r := range []*Router[*tctx]{left, right} {
		go func() {
			<-start
			done <- do(r, http.MethodGet, "/nowhere").Code
		}()
	}
	close(start)
	for range 2 {
		if code := <-done; code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", code)
		}
	}
	if do(left, http.MethodGet, "/left/ready").Code != http.StatusOK ||
		do(right, http.MethodGet, "/right/ready").Code != http.StatusOK {
		t.Fatal("a concurrently built parent omitted the shared mount")
	}
}

func TestJSONOptionsClonesCallerSlice(t *testing.T) {
	r := newTestRouter()
	opts := []json.Options{json.RejectUnknownMembers(true)}
	r.JSONOptions(opts...)
	opts[0] = nil
	if len(r.ropts.jsonOpts) != 1 || r.ropts.jsonOpts[0] == nil {
		t.Fatal("JSONOptions retained the caller's slice backing array")
	}
}

func mustPanicContaining(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		switch v := recover().(type) {
		case nil:
			t.Errorf("no panic, want one that reads %q", want)
		case string:
			if !strings.Contains(v, want) {
				t.Errorf("panic = %q, want one that holds %q", v, want)
			}
		default:
			t.Errorf("panic = %v of type %T, want a string", v, v)
		}
	}()
	fn()
}

// Route.Params is what a route table asserts against InlineParamBudget, so the
// count has to be exactly what the request path puts in Base: host parameters
// and path parameters together.
func TestRouteParamsCountsHostAndPathTogether(t *testing.T) {
	r := newTestRouter()
	r.GET("/health", echoRoute)
	r.GET("/users/{id}", echoRoute)
	r.GET("/files/{path...}", echoRoute)
	r.GET("/f/{name}.{ext}", echoRoute)
	r.GET("/assets/*", echoRoute)
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) {
		h.GET("/o/{oid}/t/{tid}/m/{mid}", echoRoute)
		h.GET("/o/{oid}/t/{tid}/m/{mid}/r/{rid}", echoRoute)
	})
	r.Host("{env}.{region}.{tenant}.example.net", func(h *Router[*tctx]) {
		h.GET("/o/{oid}", echoRoute)
	})

	want := map[string]int{
		"|/health":          0,
		"|/users/{id}":      1,
		"|/files/{path...}": 1,
		"|/f/{name}.{ext}":  2,
		"|/assets/*":        1,
		"{tenant}.example.com|/o/{oid}/t/{tid}/m/{mid}":         4,
		"{tenant}.example.com|/o/{oid}/t/{tid}/m/{mid}/r/{rid}": 5,
		"{env}.{region}.{tenant}.example.net|/o/{oid}":          4,
	}
	for _, rt := range r.Routes() {
		key := rt.Host + "|" + rt.Pattern
		w, ok := want[key]
		if !ok {
			t.Errorf("unexpected route %q", key)
			continue
		}
		if rt.Params != w {
			t.Errorf("%s: Params = %d, want %d", key, rt.Params, w)
		}
		delete(want, key)
	}
	for key := range want {
		t.Errorf("route %q never appeared", key)
	}
}

// The library's own fixtures must stay inside the budget too, so a change to
// maxInlineParams that the sample tables cannot afford fails here.
func TestSampleTablesStayInsideTheInlineBudget(t *testing.T) {
	for name, build := range map[string]func() *Router[*tctx]{
		"siteRouter": siteRouter,
	} {
		t.Run(name, func(t *testing.T) {
			for _, rt := range build().Routes() {
				if rt.Params > InlineParamBudget {
					t.Errorf("%s %s%s carries %d parameters, over the budget of %d",
						rt.Method, rt.Host, rt.Pattern, rt.Params, InlineParamBudget)
				}
			}
		})
	}
}

// scopeAnswering registers one fallback scope per prefix, each reporting its
// own prefix, so a 404 names the scope that answered it.
func scopeAnswering(r *Router[*tctx], prefix string) {
	r.Route(prefix, func(g *Router[*tctx]) {
		g.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), prefix) })
	})
}

// Overlapping scopes are ordered by comparing their prefixes segment by
// segment. The first position where two disagree decides it: a static segment
// beats a template, a template beats a regex, and a regex beats a plain
// parameter. Only when every shared position ties does the longer prefix win,
// then the deeper scope, then the lower prefix.
//
// This is the order the trie already uses to choose a route, so the scope that
// answers a miss is the scope the matching route would have sat in. It is not
// "the prefix with the most segments wins": /api answers /api/b/c even though
// /{p}/b/c constrains all three segments and /api constrains one.
func TestScopeFallbackPrecedenceIsLeftmostMostSpecificSegment(t *testing.T) {
	// The prefixes in each case are chosen so that the last tie-break, the
	// lower prefix, disagrees with specificity. A case that stops testing the
	// rule stops passing.
	cases := []struct {
		name   string
		scopes []string
		probe  string
		want   string
	}{
		{
			name:   "a static first segment beats a parameter that matches more segments",
			scopes: []string{"/api", "/{p}/b/c"},
			probe:  "/api/b/c",
			want:   "/api",
		},
		{
			name:   "a template beats a regex and a parameter",
			scopes: []string{"/{z}q", "/{n:[0-9]+}", "/{a}"},
			probe:  "/5q/missing",
			want:   "/{z}q",
		},
		{
			name:   "a regex beats a parameter",
			scopes: []string{"/{z}q", "/{n:[0-9]+}", "/{a}"},
			probe:  "/7/missing",
			want:   "/{n:[0-9]+}",
		},
		{
			name:   "a parameter answers what neither the template nor the regex covers",
			scopes: []string{"/{z}q", "/{n:[0-9]+}", "/{a}"},
			probe:  "/zz/missing",
			want:   "/{a}",
		},
		{
			name:   "the longer prefix wins once every shared segment ties",
			scopes: []string{"/a", "/a/b"},
			probe:  "/a/b/missing",
			want:   "/a/b",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Both registration orders: the order is the sort's, not the
			// caller's.
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
					r := newTestRouter()
					scopes := slices.Clone(tc.scopes)
					if reverse {
						slices.Reverse(scopes)
					}
					for _, prefix := range scopes {
						scopeAnswering(r, prefix)
					}
					rec := do(r, http.MethodGet, tc.probe)
					if rec.Code != http.StatusNotFound || rec.Body.String() != tc.want {
						t.Errorf("%s = %d %q, want 404 %q", tc.probe, rec.Code, rec.Body.String(), tc.want)
					}
				})
			}
		})
	}
}

// Two scopes can carry the same prefix by different routes. The deeper one
// wins, so a fallback set on an inner group is not shadowed by an outer group
// that happens to spell out the same path.
func TestScopeFallbackPrefersTheDeeperScopeOnAnEqualPrefix(t *testing.T) {
	r := newTestRouter()
	r.Route("/a/b", func(g *Router[*tctx]) {
		g.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "flat") })
	})
	r.Route("/a", func(g *Router[*tctx]) {
		g.Route("/b", func(h *Router[*tctx]) {
			h.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "nested") })
		})
	})

	if got := do(r, http.MethodGet, "/a/b/missing").Body.String(); got != "nested" {
		t.Errorf("body = %q, want %q", got, "nested")
	}
}

// The host comes first, before any of the specificity above: a scope belongs to
// the host it was declared under, and scopeFallback.hostIdx keeps it there. A
// request that matched a host never reaches an any-host scope, however much
// more specific that scope's prefix is.
func TestScopeFallbackNeverCrossesAHost(t *testing.T) {
	r := newTestRouter()
	scopeAnswering(r, "/a")
	r.Host("api.example.com", func(h *Router[*tctx]) {
		h.Route("/{p}", func(g *Router[*tctx]) {
			g.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "host /{p}") })
		})
	})

	if got := doHost(r, http.MethodGet, "api.example.com", "/a/missing").Body.String(); got != "host /{p}" {
		t.Errorf("host request answered by %q, want %q", got, "host /{p}")
	}
	if got := do(r, http.MethodGet, "/a/missing").Body.String(); got != "/a" {
		t.Errorf("any-host request answered by %q, want %q", got, "/a")
	}
}

// A scope keyed by nothing covers everything, so it cannot own a fallback.
func TestPrefixLessScopeCannotOwnFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		set  func(*Router[*tctx])
	}{
		{"ErrorHandler", "the error handler", func(g *Router[*tctx]) {
			g.ErrorHandler(func(c *tctx, err error) {})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			mustPanicContaining(t, "a scope without a prefix cannot own "+tc.want, func() {
				r.Group(func(g *Router[*tctx]) { tc.set(g) })
			})
		})
	}
}

// The root, a host scope and any prefixed scope all name a region the router
// can match, so each keeps its own fallbacks.
func TestScopesThatNameARegionKeepTheirFallbacks(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) {
		_ = c.String(StatusOf(err), "root "+strconv.Itoa(StatusOf(err)))
	})
	r.GET("/outside", func(c *tctx) error { return ErrInternalServerError })
	r.Route("/api", func(g *Router[*tctx]) {
		g.ErrorHandler(func(c *tctx, err error) {
			_ = c.String(StatusOf(err), "api "+strconv.Itoa(StatusOf(err)))
		})
		g.GET("/inside", func(c *tctx) error { return ErrInternalServerError })
	})
	// A nested Group inherits the prefix of its owner, so it may own them too.
	r.Route("/deep", func(g *Router[*tctx]) {
		g.Group(func(h *Router[*tctx]) {
			h.ErrorHandler(func(c *tctx, err error) {
				_ = c.String(StatusOf(err), "deep "+strconv.Itoa(StatusOf(err)))
			})
		})
	})

	for _, tc := range []struct{ path, want string }{
		{"/outside", "root 500"},
		{"/api/inside", "api 500"},
		{"/nowhere", "root 404"},
		{"/api/nowhere", "api 404"},
		{"/deep/nowhere", "deep 404"},
	} {
		if got := do(r, http.MethodGet, tc.path).Body.String(); got != tc.want {
			t.Errorf("GET %s = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// install does not run through handle, so Mount has to mark the owners itself
// or Use will pass a guard that exists to catch exactly this.
func TestUseAfterMountIsRefused(t *testing.T) {
	sub := newTestRouter()
	sub.GET("/ping", echoRoute)
	r := newTestRouter()
	r.Mount("/api", sub)
	mustPanicContaining(t, "Use must come before the routes of a scope", func() {
		r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] { return next })
	})
}

// A mounted router's fallbacks describe the region it was mounted at, so they
// answer there and leave the rest of the tree to the parent.
func TestMountKeepsTheFallbacksOfTheMountedRouter(t *testing.T) {
	api := newTestRouter()
	api.ErrorHandler(func(c *tctx, err error) {
		_ = c.String(StatusOf(err), "api "+strconv.Itoa(StatusOf(err)))
	})
	api.GET("/boom", func(*tctx) error { return ErrInternalServerError })

	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) {
		_ = c.String(StatusOf(err), "root "+strconv.Itoa(StatusOf(err)))
	})
	r.Mount("/api", api)
	r.GET("/outside", func(*tctx) error { return ErrInternalServerError })

	for _, tc := range []struct{ path, want string }{
		{"/api/nope", "api 404"},
		{"/api/boom", "api 500"},
		{"/nope", "root 404"},
	} {
		if got := do(r, http.MethodGet, tc.path).Body.String(); got != tc.want {
			t.Errorf("GET %s = %q, want %q", tc.path, got, tc.want)
		}
	}
	if got := do(r, http.MethodGet, "/outside").Code; got != http.StatusInternalServerError {
		t.Errorf("GET /outside outside the mount = %d, want 500", got)
	}
}

// These live on the router the request path reads, and there is one of those
// per served router, so a mount would drop them without a word.
func TestMountRefusesARouterCarryingRootOnlySettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		set  func(*Router[*tctx])
	}{
		{"Pre", "Pre middleware", func(s *Router[*tctx]) {
			s.Pre(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] { return next })
		}},
		{"Observe", "an observer", func(s *Router[*tctx]) {
			s.Observe(func(Context, int, int64, time.Duration, error) {})
		}},
		{"HandleOPTIONS", "HandleOPTIONS(false)", func(s *Router[*tctx]) { s.HandleOPTIONS(false) }},
		{"RedirectTrailingSlash", "RedirectTrailingSlash(true)", func(s *Router[*tctx]) { s.RedirectTrailingSlash(true) }},
		{"MaxBodyBytes", "MaxBodyBytes", func(s *Router[*tctx]) { s.MaxBodyBytes(1 << 10) }},
		{"MaxMultipartMemory", "MaxMultipartMemory", func(s *Router[*tctx]) { s.MaxMultipartMemory(1 << 10) }},
		{"JSONOptions", "JSONOptions", func(s *Router[*tctx]) { s.JSONOptions(json.Deterministic(true)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := newTestRouter()
			sub.GET("/a", echoRoute)
			tc.set(sub)
			mustPanicContaining(t, "the mounted router carries "+tc.want, func() {
				newTestRouter().Mount("/api", sub)
			})
		})
	}
}

// Mount repoints the owner of what it is given. Handed a scope, it would tear
// that scope out of the router that owns it.
func TestMountRefusesAScopeOfAnotherRouter(t *testing.T) {
	other := newTestRouter()
	var scope *Router[*tctx]
	other.Group(func(g *Router[*tctx]) {
		scope = g
		g.GET("/a", echoRoute)
	})

	mustPanicContaining(t, "Mount needs a top-level router", func() {
		newTestRouter().Mount("/m", scope)
	})
	// The router that owns the scope is untouched and still open.
	other.GET("/late", echoRoute)
	if got := do(other, http.MethodGet, "/late").Code; got != http.StatusOK {
		t.Errorf("GET /late on the owning router = %d, want 200", got)
	}
}

// Routing trims the trailing slash before matching, but a mounted handler needs
// the path the client sent: a FileServer redirects relative to it.
func TestMountedHandlerKeepsTheTrailingSlash(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	var seen string
	r := newTestRouter()
	r.MountHandler("/files", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = req.URL.Path
		http.FileServer(http.Dir(dir)).ServeHTTP(w, req)
	}))

	rec := do(r, http.MethodGet, "/files/sub/")
	if seen != "/sub/" {
		t.Errorf("path seen by the mounted handler = %q, want %q", seen, "/sub/")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("GET /files/sub/ = %d, want 200 (Location %q)", rec.Code, rec.Header().Get("Location"))
	}
	if got := rec.Body.String(); got != "<h1>hi</h1>" {
		t.Errorf("body = %q, want the index", got)
	}

	// A request without the slash still arrives without it.
	do(r, http.MethodGet, "/files/sub/index.html")
	if seen != "/sub/index.html" {
		t.Errorf("path without a trailing slash = %q, want %q", seen, "/sub/index.html")
	}
}

// The 405 branch reads the parameters off the matched route; the 404 branch has
// no route and must take them from the scope prefix.
func TestScopeFallbackSeesItsPrefixParams(t *testing.T) {
	r := newTestRouter()
	r.Route("/t/{tid}", func(g *Router[*tctx]) {
		g.ErrorHandler(func(c *tctx, err error) {
			_ = c.String(StatusOf(err), strconv.Itoa(StatusOf(err))+
				" tid="+c.Param("tid")+" pattern="+c.RoutePattern())
		})
		g.GET("/x", echoRoute)
	})

	if got := do(r, http.MethodGet, "/t/acme/missing").Body.String(); got != "404 tid=acme pattern=/t/{tid}" {
		t.Errorf("scope 404 = %q, want %q", got, "404 tid=acme pattern=/t/{tid}")
	}
	if got := do(r, http.MethodPost, "/t/acme/x").Body.String(); got != "405 tid=acme pattern=/t/{tid}/x" {
		t.Errorf("scope 405 = %q, want the route pattern and tid", got)
	}
}

// A host scope contributes parameters too, and the scope prefix appends to them
// rather than replacing them.
func TestScopeFallbackKeepsHostParamsToo(t *testing.T) {
	r := newTestRouter()
	r.Host("{sub}.example.com", func(h *Router[*tctx]) {
		h.Route("/t/{tid}", func(g *Router[*tctx]) {
			g.ErrorHandler(func(c *tctx, err error) {
				_ = c.String(StatusOf(err), "sub="+c.Param("sub")+" tid="+c.Param("tid"))
			})
			g.GET("/x", echoRoute)
		})
	})

	got := doHost(r, http.MethodGet, "acme.example.com", "/t/42/missing").Body.String()
	if got != "sub=acme tid=42" {
		t.Errorf("scope 404 under a host = %q, want %q", got, "sub=acme tid=42")
	}
}

// A path arrives canonicalised, so a literal spelled any other way is compared
// against text no request can produce.
func TestUnmatchableStaticLiteralsAreRejected(t *testing.T) {
	for _, tc := range []struct{ pattern, want string }{
		{`/backslash/a\b`, "write %5C"},
		{"/lower/a%2fb", "upper-case escapes"},
		{"/decoded/%41", `decoded, so write "A"`},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			err := ValidatePattern(tc.pattern)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidatePattern(%q) = %v, want one that reads %q", tc.pattern, err, tc.want)
			}
			mustPanicContaining(t, tc.want, func() { newTestRouter().GET(tc.pattern, echoRoute) })
		})
	}

	// A % that starts no escape reaches the trie as written, so it still works.
	if err := ValidatePattern("/discount/50%"); err != nil {
		t.Errorf("ValidatePattern(/discount/50%%) = %v, want nil", err)
	}
}

// {*} goes through the brace branch, which rejects duplicates; the bare form
// has to do the same.
func TestBareStarChecksForADuplicateName(t *testing.T) {
	if err := ValidatePattern("/{*}/*"); err == nil ||
		!strings.Contains(err.Error(), "duplicate parameter") {
		t.Errorf("ValidatePattern(/{*}/*) = %v, want a duplicate-parameter error", err)
	}
}

// Registration and the first request order against each other, so the check
// that refuses a late route cannot be overtaken by the request that makes it
// late. Without that ordering a route could go into a trie a request had
// already started reading.
func TestLateRegistrationIsRefusedNotRaced(t *testing.T) {
	for range 200 {
		r := newTestRouter()
		r.GET("/a", echoRoute)

		var wg sync.WaitGroup
		var panicked atomic.Bool
		wg.Go(func() { do(r, http.MethodGet, "/a") })
		wg.Go(func() {
			defer func() {
				if recover() != nil {
					panicked.Store(true)
				}
			}()
			r.GET("/b", echoRoute)
		})
		wg.Wait()

		// Either the route was registered before the request, or it was
		// refused. It is never installed into a trie being read.
		if !panicked.Load() && do(r, http.MethodGet, "/b").Code != http.StatusOK {
			t.Fatal("a route was neither refused nor served")
		}
	}
}
