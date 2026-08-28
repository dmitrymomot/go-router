package middleware

import (
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// MethodOverrideConfig configures [MethodOverrideWithConfig].
type MethodOverrideConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Getter returns the method that the request asks for, or an empty string.
	// It defaults to [MethodFromHeader] on X-HTTP-Method-Override.
	Getter func(c router.Context) string
}

// MethodOverride is [MethodOverrideWithConfig] with its default config, which
// reads the X-HTTP-Method-Override header. It is a middleware itself, so it
// goes into Pre without a call:
//
//	r.Pre(middleware.MethodOverride[Ctx])
func MethodOverride[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return MethodOverrideWithConfig[C](MethodOverrideConfig{})(next)
}

// MethodOverrideWithConfig turns a POST into the method that the request names.
//
// Add it with [router.Router.Pre], which is the stage that runs before the
// router matches. [router.Router.Use] runs after the match, where the method
// has already picked the route and a new one changes nothing:
//
//	r.Pre(middleware.MethodOverrideWithConfig[Ctx](middleware.MethodOverrideConfig{
//		Getter: middleware.MethodFromForm("_method"),
//	}))
//
// An HTML form sends GET and POST and nothing else, so this is how a page
// reaches DELETE /posts/{id} without a line of JavaScript:
//
//	<form method="post" action="/posts/7">
//		<input type="hidden" name="_method" value="DELETE">
//	</form>
//
// It only ever upgrades a POST, and only to PUT, PATCH or DELETE. A request of
// any other method passes through untouched, and so does a POST that names
// anything else: a POST that turned into a GET would leave a request that a
// cache stores and a proxy repeats, which is the opposite of what the client
// asked for.
func MethodOverrideWithConfig[C router.Context](cfg MethodOverrideConfig) router.Middleware[C] {
	if cfg.Getter == nil {
		cfg.Getter = MethodFromHeader(router.HeaderXHTTPMethodOverride)
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			req := c.Request()
			if req.Method != http.MethodPost || skipped(cfg.Skip, c) {
				return next(c)
			}

			method := strings.ToUpper(strings.TrimSpace(cfg.Getter(c)))
			if !overridable(method) {
				return next(c)
			}

			overridden := *req
			overridden.Method = method
			c.SetRequest(&overridden)
			return next(c)
		}
	}
}

// MethodFromHeader reads the method from a header:
//
//	Getter: middleware.MethodFromHeader(router.HeaderXHTTPMethodOverride)
//
// A client that writes its own request sends the header, which is the reading
// that costs nothing: no body is read, and the value cannot come from a form
// that a page on another site submits.
func MethodFromHeader(name string) func(router.Context) string {
	return func(c router.Context) string { return c.Request().Header.Get(name) }
}

// MethodFromForm reads the method from a field of the posted form:
//
//	Getter: middleware.MethodFromForm("_method")
//
// It is what an HTML form needs, because a form sets no headers. Reading it
// parses the body, which has two consequences. The body reaches the handler
// through the form that net/http parsed and cached, so [router.Base.BindForm]
// still answers; and the limits of the router no longer apply to that parse,
// so pair it with [BodyLimit], which caps the body itself.
func MethodFromForm(field string) func(router.Context) string {
	return func(c router.Context) string { return c.Request().PostFormValue(field) }
}

// MethodFromQuery reads the method from a query parameter:
//
//	Getter: middleware.MethodFromQuery("_method")
//
// It reads no body, so it fits a form that posts a file. The method then sits
// in the action of the form, where a link and a log line carry it too.
func MethodFromQuery(param string) func(router.Context) string {
	return func(c router.Context) string { return c.Request().URL.Query().Get(param) }
}

// overridable reports whether a POST may become m. A form reaches the three
// methods that a page has no other way to send.
func overridable(m string) bool {
	switch m {
	case http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
