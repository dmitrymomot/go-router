package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tctx is the application context that the tests use.
type tctx struct {
	Base
	Tag string
}

func newTestRouter() *Router[*tctx] {
	return New(func(http.ResponseWriter, *http.Request) *tctx { return &tctx{Tag: "app"} })
}

// echoRoute writes the pattern and every parameter, so one assertion covers
// both the match and the parameter values.
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
		{"/a/b/c", "/a/{x}/c x=b"}, // static "b" fails deeper, the walk backtracks
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
	// The prefix parameter stays readable inside the mounted router.
	if got, want := rec.Body.String(), "/tenants/{tid}/api/users/{id} tid=acme id=7"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestMountHandlerStripsThePrefix(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "path=%s", r.URL.Path)
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

func TestErrorHandling(t *testing.T) {
	r := newTestRouter()
	r.GET("/boom", func(*tctx) error { return ErrForbidden.WithMessage("no entry") })
	r.GET("/internal", func(*tctx) error { return fmt.Errorf("database is down") })

	rec := do(r, http.MethodGet, "/boom")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if got, want := rec.Body.String(), `{"status":403,"error":"no entry"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	// A browser asks for HTML, so the same error comes back as text.
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

func TestCustomFallbacks(t *testing.T) {
	r := newTestRouter()
	r.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "nothing here") })
	r.MethodNotAllowed(func(c *tctx) error { return c.String(http.StatusMethodNotAllowed, "wrong method") })
	r.GET("/only", echoRoute)

	if got := do(r, http.MethodGet, "/other").Body.String(); got != "nothing here" {
		t.Errorf("not found body = %q", got)
	}
	if got := do(r, http.MethodPost, "/only").Body.String(); got != "wrong method" {
		t.Errorf("method not allowed body = %q", got)
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

func TestWrapHandlerAndWrapMiddleware(t *testing.T) {
	// A standard middleware that both replaces the request and wraps the
	// response writer, which is the shape that exercises the adapter.
	tagging := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(&prefixWriter{ResponseWriter: w, prefix: "["},
				req.WithContext(context.WithValue(req.Context(), ctxKey("who"), "std")))
		})
	}

	r := newTestRouter()
	r.Use(WrapMiddleware[*tctx](tagging))
	r.GET("/plain", WrapHandler[*tctx](http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			fmt.Fprint(w, req.Context().Value(ctxKey("who")))
		})))

	rec := do(r, http.MethodGet, "/plain")
	if got, want := rec.Body.String(), "[std"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

type ctxKey string

// prefixWriter writes a prefix before the first body byte, the way a standard
// middleware wraps the writer it was handed.
type prefixWriter struct {
	http.ResponseWriter
	prefix string
	done   bool
}

func (w *prefixWriter) Write(b []byte) (int, error) {
	if !w.done {
		w.done = true
		//nolint:errcheck // test helper
		w.ResponseWriter.Write([]byte(w.prefix))
	}
	return w.ResponseWriter.Write(b)
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

	// The panic reaches the handler with its stack in the internal cause.
	seen = nil
	do(r, http.MethodGet, "/panic")
	if !strings.Contains(seen[0].msg, "panic: boom") {
		t.Errorf("error = %q, want the panic value", seen[0].msg)
	}
	if !strings.Contains(seen[0].msg, "router.(*Router[") && !strings.Contains(seen[0].msg, "goroutine") {
		t.Errorf("error = %q, want a stack", seen[0].msg)
	}
}

func TestFallbackHandlersBeatTheErrorHandler(t *testing.T) {
	errorHandlerRan := false

	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) {
		errorHandlerRan = true
		_ = c.String(StatusOf(err), "error handler")
	})
	r.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "not found") })
	r.MethodNotAllowed(func(c *tctx) error {
		return c.String(http.StatusMethodNotAllowed, "method not allowed")
	})
	r.GET("/only", echoRoute)

	if got := do(r, http.MethodGet, "/missing").Body.String(); got != "not found" {
		t.Errorf("body = %q, want %q", got, "not found")
	}
	if got := do(r, http.MethodPost, "/only").Body.String(); got != "method not allowed" {
		t.Errorf("body = %q, want %q", got, "method not allowed")
	}
	if errorHandlerRan {
		t.Error("the error handler ran; the fallback handlers must win")
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
