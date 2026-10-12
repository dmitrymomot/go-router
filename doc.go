// Package router implements an HTTP router whose handlers take an
// application-defined request context and return an error.
//
// # The context
//
// An application declares a struct that embeds [Base] and adds what its
// handlers need, such as a database or the signed-in user. Every handler and
// middleware takes that type, and [New] or [NewPooled] builds one per request:
//
//	type Context struct {
//		router.Base
//		DB *sql.DB
//	}
//
//	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
//		return &Context{DB: db}
//	})
//	r.GET("/users/{id}", func(c *Context) error {
//		return c.String(http.StatusOK, "user "+c.Param("id"))
//	})
//
// The router fills the embedded Base before it calls the handler. Base is also
// a [context.Context] that ends with the request, so a handler passes c on to
// the database. A [Router] is an [http.Handler], and every setter leaves it
// ready to serve: a bad pattern or setting panics at the call that made it.
//
// # Routing
//
// A pattern names a parameter in braces, "/users/{id}", which [Base.Param]
// and [Base.ParamAs] read; a parameter may sit inside a segment, as in
// "/reports/rep-{date}.csv". A constraint narrows a parameter: the classes
// "{id:int}", "{t:slug}" and "{id:uuid}", a class of your own from
// [Router.ParamClass], or a regular expression. A value outside it does not
// match. A trailing "{name...}" takes the rest of the path. See
// [Router.Handle].
//
// A route takes the path as sent, so "/users/" does not reach "/users" unless
// [Router.RedirectTrailingSlash] is on. A catch-all takes both, and
// [Base.TrailingSlash] tells them apart.
//
// [Router.Group] opens a scope for middleware that covers some routes,
// [Router.Route] a scope under a prefix, and [Router.Host] a scope for a
// host pattern such as "{tenant}.example.com". [Router.Mount] grafts a router
// of the same context type, and [Router.MountHandler] any [http.Handler],
// a router with a context type of its own included. [Expand] builds a link
// from a pattern, and [Router.Routes] lists the table.
//
// # Errors
//
// A handler returns an error, and the error handler of its scope answers it.
// An [HTTPError] names the status: return a sentinel such as [ErrNotFound],
// or one refined with [HTTPError.WithMessage] and [HTTPError.WithError]. A
// domain error names its own status by implementing [StatusCoder]. Anything
// else is a 500, and its text reaches only the log unless the error handler
// was built to expose the cause.
//
// [Router.ErrorHandler] sets the handler of a scope: [TextErrorHandler],
// the default, [JSONErrorHandler], [ProblemErrorHandler] for an RFC 9457
// problem document, or one of your own. A handler of your own calls
// [ResetForError] first, so an error does not go out with the cache headers
// or the file name of the response that failed. [HandleError] answers an
// error inside a middleware, which then sees the final status.
//
// # Binding
//
// [Base.Bind] fills a struct from the request, each field from the source its
// tag names: `json` or `form` for the body, `param`, `query` and `header`.
// [Base.BindJSON], [Base.BindForm], [Base.BindQuery], [Base.BindPath] and
// [Base.BindHeader] read one source each. Outside JSON, a tag takes `,split`
// for a comma list, and a `default:"…"` tag fills an absent key; in the query
// and a form, dotted keys fill a nested struct and bracket keys a map.
// [Router.Decoder] adds a body type, such as XML. A bound type that implements
// [Validator] is validated last, and [FieldErrorsOf] reads the fields an error
// names.
//
// # Responses
//
// [Base.JSON], [Base.String], [Base.HTML], [Base.Blob] and [Base.Stream]
// write a body; [Base.Render] writes a [Component], such as a templ component
// or an html/template call in a [ComponentFunc]. [Base.File] sends a file
// from an [io/fs.FS]. [Base.LocalRedirect] refuses a target off the site,
// which closes the open redirect of a "next" parameter. [Base.CheckETag]
// answers 304 to a client that already holds the entity of [ETagOf].
//
// # Middleware
//
// A [Middleware] wraps a [HandlerFunc] and returns another:
//
//	func requireUser(next router.HandlerFunc[*Context]) router.HandlerFunc[*Context] {
//		return func(c *Context) error {
//			if c.Cookie("session") == "" {
//				return router.ErrUnauthorized
//			}
//			return next(c)
//		}
//	}
//
// [Router.Use] runs middleware in the order given, the first one outermost,
// for the routes registered after it; a route takes middleware of its own as
// the last arguments of [Router.GET] and the rest. The middleware of a scope
// with a prefix or a host also wraps its 404 and 405 answers, and
// [Router.Pre] runs before matching. Package
// [github.com/dmitrymomot/go-router/middleware] holds the middleware that
// ships with the router and the order they fit in.
//
// # Testing
//
// Package [github.com/dmitrymomot/go-router/routertest] sends requests to a
// router or a handler and checks the answers, and builds a context of your
// own type for a handler called alone.
//
// # Serving
//
// Package [github.com/dmitrymomot/go-router/serve] runs the router with
// timeouts and a graceful drain that ends with the context it was given.
package router
