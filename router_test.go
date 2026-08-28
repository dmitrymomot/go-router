package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
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

// TestAllowHeaderNamesExactlyTheMethodsThatAnswer checks the Allow header
// against the router that sent it: every method it names answers the request,
// and every method it leaves out produces the 405 that carries it.
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

// TestASentinelRouteNeverProducesA405 pins the reason the sentinel of
// [Router.Any] contributes nothing to the Allow header: a node that carries
// one answers whatever arrives, so no request reaches the 405 that would have
// to name every method.
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
			_, _ = fmt.Fprint(w, req.Context().Value(ctxKey("who")))
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
	// The mounted router re-matches the stripped path, so a parameter of the
	// prefix does not cross the seam. Use Mount and one context type when the
	// prefix parameter has to reach the mounted routes.
	want := `tenant="" id="7" path="/users/7"`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// setHeader marks the response, so that an assertion can tell which middleware
// chain ran around a fallback.
func setHeader(name, value string) Middleware[*tctx] {
	return func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			c.Response().Header().Set(name, value)
			return next(c)
		}
	}
}

// TestMatchedRouteReachesAWrappedHandler covers the labels that a standard
// middleware reads: the pattern on the request, which keeps a tracing span out
// of unbounded cardinality, and the parameters that PathValue answers.
func TestMatchedRouteReachesAWrappedHandler(t *testing.T) {
	var seen []string
	std := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen = append(seen, "mw "+req.Pattern+" id="+req.PathValue("id"))
			next.ServeHTTP(w, req)
		})
	}

	r := newTestRouter()
	r.Use(WrapMiddleware[*tctx](std))
	r.GET("/users/{id}/posts/{postID}", WrapHandler[*tctx](http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintf(w, "handler %s id=%s postID=%s",
				req.Pattern, req.PathValue("id"), req.PathValue("postID"))
		})))

	rec := do(r, http.MethodGet, "/users/7/posts/12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if want := []string{"mw /users/{id}/posts/{postID} id=7"}; !slices.Equal(seen, want) {
		t.Errorf("middleware saw %q, want %q", seen, want)
	}
	want := "handler /users/{id}/posts/{postID} id=7 postID=12"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestRequestPatternIsSetForAFallbackToo pins the label of a request that no
// route answered: a 405 carries the pattern whose method did not fit, and a
// 404 carries none, because no pattern matched.
func TestRequestPatternIsSetForAFallbackToo(t *testing.T) {
	var pattern string
	r := newTestRouter()
	r.Use(WrapMiddleware[*tctx](func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			pattern = req.Pattern
			next.ServeHTTP(w, req)
		})
	}))
	r.GET("/users/{id}", echoRoute)

	if do(r, http.MethodPost, "/users/7"); pattern != "/users/{id}" {
		t.Errorf("405 labelled %q, want %q", pattern, "/users/{id}")
	}
	pattern = "unset"
	if do(r, http.MethodGet, "/nothing"); pattern != "" {
		t.Errorf("404 labelled %q, want an empty pattern", pattern)
	}
}

// ---------------------------------------------------------------------------
// The pre-routing stage
// ---------------------------------------------------------------------------

// rewritePath is the shape of Pre middleware that the stage exists for: it
// replaces the request, and the trie then matches the path it left behind.
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

// TestPreRunsBeforeTheRoutingMiddleware pins the order of the two stages and
// the state that a Pre middleware sees, which is a request that nothing has
// matched yet.
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

// TestPreRunsForAFallbackToo checks that the stage is not tied to a match: a
// path that no route answers still passes through it.
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

// TestPreErrorReachesTheErrorHandler covers a Pre middleware that answers by
// itself, which is what an authentication gate in front of the trie does.
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

// TestPreDecidesWhatRedirectTrailingSlashSees documents the interaction: the
// matcher normalizes the path that the stage left behind, so the redirect
// points at the rewritten URL and not at the one the client sent.
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

// ---------------------------------------------------------------------------
// Prefix-scoped fallbacks
// ---------------------------------------------------------------------------

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

// TestPrefixScopeFallbackPicksTheInnermostScope checks the longest matching
// prefix, and that the chain of the inner scope still carries the middleware of
// the outer one.
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
		want [3]string // root, api, v1
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

// TestPrefixScopeFallbackCoversAParameterPrefix checks that a prefix with a
// parameter in it still owns the fallbacks below it.
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

// TestPrefixScopeFallbackUsesTheHandlerOfItsHost checks that the scope
// contributes its middleware and the host still chooses the handler.
func TestPrefixScopeFallbackUsesTheHandlerOfItsHost(t *testing.T) {
	r := newTestRouter()
	r.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "root 404") })
	r.Host("api.example.com", func(h *Router[*tctx]) {
		h.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "api 404") })
		h.Route("/v1", func(g *Router[*tctx]) {
			g.Use(setHeader("X-Scope", "v1"))
			g.GET("/users", echoRoute)
		})
	})

	rec := doHost(r, http.MethodGet, "api.example.com", "/v1/typo")
	if got := rec.Body.String(); got != "api 404" {
		t.Errorf("body = %q, want %q", got, "api 404")
	}
	if got := rec.Header().Get("X-Scope"); got != "v1" {
		t.Errorf("X-Scope = %q, want %q", got, "v1")
	}

	// The same path on a host that no scope claims keeps the root handler and
	// runs no scope middleware, because the scope belongs to the other host.
	rec = doHost(r, http.MethodGet, "other.example.com", "/v1/typo")
	if got := rec.Body.String(); got != "root 404" {
		t.Errorf("body = %q, want %q", got, "root 404")
	}
	if got := rec.Header().Get("X-Scope"); got != "" {
		t.Errorf("X-Scope = %q, want none", got)
	}
}

// TestMountedRoutesKeepTheFallbackOfTheirPrefix covers the shim that Mount
// opens, which carries the prefix of the mounted routes.
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

// ---------------------------------------------------------------------------
// Names and metadata
// ---------------------------------------------------------------------------

func TestRoutesReportsTheNameAndTheMetadata(t *testing.T) {
	type op struct{ Summary string }

	r := newTestRouter()
	r.Name("user").Meta(op{Summary: "read a user"}).GET("/users/{id}", echoRoute)
	r.Meta(op{Summary: "create a user"}).POST("/users", echoRoute)
	r.GET("/health", echoRoute)

	got := r.Routes()
	want := []Route{
		{Method: http.MethodGet, Pattern: "/health"},
		{Method: http.MethodPost, Pattern: "/users", Meta: op{Summary: "create a user"}},
		{Method: http.MethodGet, Pattern: "/users/{id}", Name: "user", Meta: op{Summary: "read a user"}},
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

// TestNameAndMetaComposeInEitherOrder pins that the two describe one route
// rather than opening a scope each.
func TestNameAndMetaComposeInEitherOrder(t *testing.T) {
	r := newTestRouter()
	r.Meta("first").Name("a").GET("/a", echoRoute)
	r.Name("b").Meta("second").GET("/b", echoRoute)

	for _, rt := range r.Routes() {
		switch rt.Pattern {
		case "/a":
			if rt.Name != "a" || rt.Meta != "first" {
				t.Errorf("/a = %+v, want the name a and the meta first", rt)
			}
		case "/b":
			if rt.Name != "b" || rt.Meta != "second" {
				t.Errorf("/b = %+v, want the name b and the meta second", rt)
			}
		}
	}
}

// TestMetaScopeCarriesEveryRouteOnIt checks that Meta, unlike Name, is not
// spent by the first route.
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

// ---------------------------------------------------------------------------
// Any, QUERY and Build
// ---------------------------------------------------------------------------

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
			// A route that answers every method produces no 405, so nothing
			// claims a method that the route does not answer.
			if rec := do(r, "PROPFIND", "/x"); rec.Code != http.StatusOK || rec.Header().Get(HeaderAllow) != "" {
				t.Errorf("PROPFIND: status = %d, Allow = %q, want 200 and no Allow header", rec.Code, rec.Header().Get(HeaderAllow))
			}
		})
	}
}

// TestAnyServesHeadFromTheGetRouteFirst pins the order of the two fallbacks:
// the GET handler answers a HEAD before the Any entry does. A HEAD answer
// carries no body, so the handler records which one ran.
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

	// A QUERY request carries a body, and Bind still reads the query string.
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

func TestBuildReportsWhatTheRouterWouldPanicOn(t *testing.T) {
	tests := []struct {
		name  string
		build func(*Router[*tctx])
		want  string
	}{
		{"a valid table", func(r *Router[*tctx]) {
			r.GET("/users/{id}", echoRoute)
			r.POST("/users", echoRoute)
		}, ""},
		{"a duplicate route", func(r *Router[*tctx]) {
			r.GET("/a", echoRoute)
			r.GET("/a", echoRoute)
		}, "already registered"},
		{"a malformed pattern", func(r *Router[*tctx]) {
			r.GET("/files/{path...}/x", echoRoute)
		}, "catch-all must be the last segment"},
		{"conflicting parameter names", func(r *Router[*tctx]) {
			r.GET("/users/{id}", echoRoute)
			r.GET("/users/{uid}/posts", echoRoute)
		}, "already named"},
		{"a router mounted inside itself", func(r *Router[*tctx]) {
			r.Mount("/self", r)
		}, "mounted inside itself"},
		{"a duplicate route name", func(r *Router[*tctx]) {
			r.Name("dup").GET("/a", echoRoute)
			r.Name("dup").GET("/b", echoRoute)
		}, `the route name "dup" names both`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			tc.build(r)

			err := r.Build()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("Build() = %v, want nil", err)
			case tc.want == "":
				return
			case err == nil:
				t.Fatalf("Build() = nil, want an error that mentions %q", tc.want)
			case !strings.Contains(err.Error(), tc.want):
				t.Errorf("Build() = %v, want an error that mentions %q", err, tc.want)
			}
			// The failure sticks, so a request cannot slip past a table that
			// never compiled.
			if again := r.Build(); again == nil || again.Error() != err.Error() {
				t.Errorf("the second Build() = %v, want the first one, %v", again, err)
			}
		})
	}
}

func TestBuildCompilesTheTableItself(t *testing.T) {
	r := newTestRouter()
	r.GET("/a", echoRoute)

	if err := r.Build(); err != nil {
		t.Fatalf("Build() = %v", err)
	}
	// Build compiled the trie, so the router refuses a later route the way the
	// first request does.
	defer func() {
		if rec := recover(); rec == nil {
			t.Error("no panic, want one that refuses a route after the router compiled")
		}
	}()
	r.GET("/b", echoRoute)
}

func TestServeHTTPPanicsOnATableThatBuildRejects(t *testing.T) {
	r := newTestRouter()
	r.GET("/a", echoRoute)
	r.GET("/a", echoRoute)

	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("no panic, want the conflict")
		}
		if msg := fmt.Sprint(got); !strings.Contains(msg, "already registered") {
			t.Errorf("panic = %q, want the conflict", msg)
		}
	}()
	do(r, http.MethodGet, "/a")
}

// ---------------------------------------------------------------------------
// The observer
// ---------------------------------------------------------------------------

// record is one call of the observer.
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
			// The record describes the answer that the client read, so it is
			// checked against the recorder rather than against a literal.
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

// TestObserveReportsTheStatusTheClientSaw covers the half that ResolveStatus
// answers from the response rather than from the error.
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

// TestObservePoolsTheContextAfterItReads checks that the seam does not hand a
// context back to the pool before the observer has read it.
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
		{"a second route on a named scope", func() {
			r := newTestRouter()
			named := r.Name("both")
			named.GET("/a", echoRoute)
			named.POST("/b", echoRoute)
		}, "already registered a route"},
		{"an empty name", func() { newTestRouter().Name("") }, "Name needs a name"},
		{"a duplicate name", func() {
			r := newTestRouter()
			r.Name("dup").GET("/a", echoRoute)
			r.Name("dup").GET("/b", echoRoute)
			r.Routes()
		}, "names both"},
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

// TestObserveReportsARequestThatThePreStageAnswered covers the two stages
// together: the observer measures the whole request, including the middleware
// that ran in front of the matcher.
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

// TestScopeFallbacksAnswerTheirScopeAlone pins the fallbacks of a path scope to
// the paths of that scope. They used to be written into the slots of the root,
// so the last scope registered answered every miss of the router and the
// handler of the root was lost.
func TestScopeFallbacksAnswerTheirScopeAlone(t *testing.T) {
	text := func(status int, body string) HandlerFunc[*tctx] {
		return func(c *tctx) error { return c.String(status, body) }
	}

	r := newTestRouter()
	r.NotFound(text(http.StatusNotFound, "root 404"))
	r.MethodNotAllowed(text(http.StatusMethodNotAllowed, "root 405"))
	r.Route("/api", func(g *Router[*tctx]) {
		g.NotFound(text(http.StatusNotFound, "api 404"))
		g.MethodNotAllowed(text(http.StatusMethodNotAllowed, "api 405"))
		g.GET("/users", echoRoute)
	})
	r.Route("/admin", func(g *Router[*tctx]) {
		g.NotFound(text(http.StatusNotFound, "admin 404"))
		g.MethodNotAllowed(text(http.StatusMethodNotAllowed, "admin 405"))
		g.GET("/panel", echoRoute)
	})
	r.GET("/health", echoRoute)

	tests := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodGet, "/api/typo", "api 404"},
		{http.MethodGet, "/admin/typo", "admin 404"},
		{http.MethodGet, "/typo", "root 404"},
		{http.MethodPost, "/api/users", "api 405"},
		{http.MethodPost, "/admin/panel", "admin 405"},
		{http.MethodPost, "/health", "root 405"},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if got := do(r, tc.method, tc.path).Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestASingleScopeFallbackLeavesTheRestOfTheRouterAlone is the same defect in
// the shape that needs no second scope: one scope handler used to escape to
// every path of the router.
func TestASingleScopeFallbackLeavesTheRestOfTheRouterAlone(t *testing.T) {
	r := newTestRouter()
	r.Route("/api", func(g *Router[*tctx]) {
		g.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "api 404") })
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

// TestAScopeFallbackReachesTheScopesBelowIt pins that a scope inside another
// one inherits the fallback of the scope that encloses it, rather than falling
// straight back to the root.
func TestAScopeFallbackReachesTheScopesBelowIt(t *testing.T) {
	r := newTestRouter()
	r.Route("/api", func(g *Router[*tctx]) {
		g.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "api 404") })
		g.Route("/v1", func(v *Router[*tctx]) { v.GET("/users", echoRoute) })
	})

	if got := do(r, http.MethodGet, "/api/v1/typo").Body.String(); got != "api 404" {
		t.Errorf("GET /api/v1/typo = %q, want %q", got, "api 404")
	}
}

// TestScopeErrorHandlerAnswersItsScopeAlone pins the error handler of a path
// scope to that scope. It used to replace the one of the root, so a handler
// that a debug branch installed rendered the failures of every route, and the
// internal cause of an unrelated request reached the client with it.
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

// TestRedirectTrailingSlashNeverPointsAtAnotherHost covers the open redirect
// that a path of "//evil.com/" produced: a Location that begins with "//" is a
// network-path reference, which a browser resolves against the current scheme
// and follows to another origin.
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

// TestAllowHeaderNamesTheMethodsOfEveryMatchingRoute covers the methods that
// backtracking would have served. A literal sibling used to hide the catch-all
// or the parameter route underneath it, so the header left out methods that
// the router answers, and a CORS preflight blocked a request that returns 200.
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

// TestAMountedRouterKeepsTheFallbacksOfTheRoot pins the documented rule that
// the root supplies the fallbacks of a mounted router. The defaults that New
// left on the mounted router used to be written over the ones the application
// chose, so mounting silently replaced them.
func TestAMountedRouterKeepsTheFallbacksOfTheRoot(t *testing.T) {
	sub := newTestRouter()
	sub.GET("/users", echoRoute)

	r := newTestRouter()
	r.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "root 404") })
	r.ErrorHandler(func(c *tctx, err error) { _ = c.String(http.StatusTeapot, "root: "+err.Error()) })
	r.Mount("/api", sub)
	r.GET("/boom", func(*tctx) error { return errors.New("kettle") })

	if got := do(r, http.MethodGet, "/api/typo").Body.String(); got != "root 404" {
		t.Errorf("GET /api/typo = %q, want %q", got, "root 404")
	}
	if got := do(r, http.MethodGet, "/typo").Body.String(); got != "root 404" {
		t.Errorf("GET /typo = %q, want %q", got, "root 404")
	}
	rec := do(r, http.MethodGet, "/boom")
	if rec.Code != http.StatusTeapot {
		t.Errorf("GET /boom: status = %d, want %d", rec.Code, http.StatusTeapot)
	}
}

// TestAGroupInsideAPrefixOwnsTheFallbackOfThatPrefix covers the scope that
// carries no prefix of its own: its fallback answers the prefix it sits under,
// and no path outside it.
func TestAGroupInsideAPrefixOwnsTheFallbackOfThatPrefix(t *testing.T) {
	r := newTestRouter()
	r.Route("/api", func(g *Router[*tctx]) {
		g.Group(func(h *Router[*tctx]) {
			h.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "api 404") })
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

// TestAScopeErrorHandlerInsideAHostAnswersThatHostAlone pins that a path scope
// of one host does not render the failures of another host.
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
