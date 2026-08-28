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
//
// Reading no body has a cost of its own on a urlencoded request. net/http
// parses that body for POST, PUT and PATCH alone, so an override that turned
// the POST into a DELETE leaves it unparsed: [router.Base.FormValue] answers
// with nothing and [FromForm] finds no token, which reads as a 403. Send the
// token in a header, post a multipart body, which parses whatever the method
// is, or read the method with [MethodFromForm], whose own parse runs first and
// leaves the fields where the handler finds them.
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
// still answers; and the parse runs under [router.Router.MaxBodyBytes] and
// [router.Router.MaxMultipartMemory], as every other form read of the router
// does. A body past either cap leaves the field unread, so the request keeps
// the method it came with, and the first form read of the handler reports the
// 413.
//
// [BodyLimit] caps the body too, but only from [router.Router.Pre], the stage
// that this middleware runs in. A [BodyLimit] in Use is installed after the
// route is matched, which is after this parse has read the body:
//
//	r.Pre(middleware.BodyLimit[Ctx](64<<10), middleware.MethodOverrideWithConfig[Ctx](...))
func MethodFromForm(field string) func(router.Context) string {
	return func(c router.Context) string {
		if r, ok := c.(formReader); ok {
			return r.FormValue(field)
		}
		// A context that answers FormValue with a signature of its own still
		// gets a bounded parse, at the default of the library rather than at
		// the limit that this router carries, which is not reachable here.
		req := c.Request()
		if req.Body != nil {
			req.Body = http.MaxBytesReader(c.Response(), req.Body, router.DefaultMaxBodyBytes)
		}
		return req.PostFormValue(field)
	}
}

// MethodFromQuery reads the method from a query parameter:
//
//	Getter: middleware.MethodFromQuery("_method")
//
// It reads no body, so it fits a form that posts a file. The method then sits
// in the action of the form, where a link and a log line carry it too.
//
// It leaves a urlencoded body unparsed for the same reason [MethodFromHeader]
// does, and the way out is the same.
func MethodFromQuery(param string) func(router.Context) string {
	return func(c router.Context) string { return c.Request().URL.Query().Get(param) }
}

// formReader is the form read of [router.Base]. Every [router.Context] answers
// it, because [router.Context] names an unexported method and only a type that
// embeds the Base has one. Reading the field through the Base keeps this parse
// under [router.Router.MaxBodyBytes] and [router.Router.MaxMultipartMemory],
// and it leaves the failure of a body past either on the context, where the
// first form read of the handler reports it.
type formReader interface{ FormValue(name string) string }

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
