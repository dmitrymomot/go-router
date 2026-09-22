package middleware

import (
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// ParseFormConfig configures [ParseFormWithConfig].
type ParseFormConfig[C router.Context] struct {
	Skip func(c C) bool
}

// ParseForm parses a URL-encoded or multipart form body before the handler
// runs, and refuses one that does not parse with [router.ErrBadRequest], or
// with [router.ErrPayloadTooLarge] when it is over the cap. The form readers
// of the handler would otherwise read such a body as empty, and a checkbox as
// off. Every other request passes through.
//
// ParseForm reads under the cap in force when it runs, so put it after
// [BodyLimit]. On a scope that takes large multipart bodies, put it after the
// authentication too, since it reads the body, and may spill it to disk,
// before the handler decides anything. Skip a route that streams its body
// through MultipartReader.
//
// It reads the body of any method, as the form readers of the router do.
//
// See Order in the package doc for where it goes.
func ParseForm[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return ParseFormWithConfig(ParseFormConfig[C]{})(next)
}

// ParseFormWithConfig is [ParseForm] with a configuration.
func ParseFormWithConfig[C router.Context](cfg ParseFormConfig[C]) router.Middleware[C] {
	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			req := c.Request()
			if skipped(cfg.Skip, c) || req.Body == nil || req.Body == http.NoBody ||
				!isFormType(req.Header.Get(router.HeaderContentType)) {
				return next(c)
			}
			if b, ok := router.FromContext(c); ok {
				if _, err := b.FormValues(); err != nil {
					return err
				}
			}
			return next(c)
		}
	}
}

func isFormType(contentType string) bool {
	ct, _, _ := strings.Cut(contentType, ";")
	ct = strings.TrimSpace(ct)
	return strings.EqualFold(ct, router.MIMEApplicationForm) ||
		strings.EqualFold(ct, router.MIMEMultipartForm)
}
