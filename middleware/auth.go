package middleware

import (
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/dmitrymomot/go-router"
)

const DefaultRealm = "Restricted"

const basicScheme = "Basic"

var (
	errMissingKey         = router.ErrUnauthorized.WithMessage("missing key")
	errInvalidKey         = router.ErrUnauthorized.WithMessage("invalid key")
	errMissingCredentials = router.ErrUnauthorized.WithMessage("missing credentials")
	errInvalidCredentials = router.ErrUnauthorized.WithMessage("invalid credentials")
	errMalformedBasicAuth = router.ErrBadRequest.WithMessage("malformed credentials")
)

func SecureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type KeyAuthConfig[C router.Context] struct {
	Skip                   func(c router.Context) bool
	Sources                []TokenSource
	Validator              func(c C, key string) (bool, error)
	OnError                func(c C, err error) error
	ContinueOnIgnoredError bool
}

var defaultKeyAuthSources = []TokenSource{FromHeader(router.HeaderAuthorization, "Bearer ")}

func KeyAuth[C router.Context](v func(c C, key string) (bool, error)) router.Middleware[C] {
	return KeyAuthWithConfig(KeyAuthConfig[C]{Validator: v})
}

func KeyAuthWithConfig[C router.Context](cfg KeyAuthConfig[C]) router.Middleware[C] {
	if cfg.Validator == nil {
		panic("middleware: KeyAuthConfig needs a Validator")
	}
	if len(cfg.Sources) == 0 {
		cfg.Sources = defaultKeyAuthSources
	}
	checkTokenSources("KeyAuthConfig", cfg.Sources)

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			var verr error
			presented := false
			for key := range readTokens(c, cfg.Sources) {
				presented = true
				ok, err := cfg.Validator(c, key)
				if ok && err == nil {
					return next(c)
				}
				if err != nil && verr == nil {
					verr = err
				}
			}

			failure := error(errMissingKey)
			switch {
			case verr != nil:
				failure = verr
			case presented:
				failure = errInvalidKey
			}

			if cfg.OnError == nil {
				return failure
			}
			if err := cfg.OnError(c, failure); err != nil {
				return err
			}
			if cfg.ContinueOnIgnoredError {
				return next(c)
			}
			return nil
		}
	}
}

type BasicAuthConfig[C router.Context] struct {
	Skip      func(c router.Context) bool
	Validator func(c C, user, pass string) (bool, error)
	Realm     string
}

func BasicAuth[C router.Context](v func(c C, user, pass string) (bool, error)) router.Middleware[C] {
	return BasicAuthWithConfig(BasicAuthConfig[C]{Validator: v})
}

func BasicAuthWithConfig[C router.Context](cfg BasicAuthConfig[C]) router.Middleware[C] {
	if cfg.Validator == nil {
		panic("middleware: BasicAuthConfig needs a Validator")
	}
	if cfg.Realm == "" {
		cfg.Realm = DefaultRealm
	}
	challenge := basicScheme + " realm=" + strconv.Quote(cfg.Realm)

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			scheme, rest, found := strings.Cut(
				c.Request().Header.Get(router.HeaderAuthorization), " ")
			rest = strings.TrimSpace(rest)

			if !found || !strings.EqualFold(scheme, basicScheme) || rest == "" {
				return challengeWith(c, challenge, errMissingCredentials)
			}

			raw, err := base64.StdEncoding.DecodeString(rest)
			if err != nil {
				return errMalformedBasicAuth.WithError(err)
			}
			user, pass, found := strings.Cut(string(raw), ":")
			if !found {
				return errMalformedBasicAuth
			}

			ok, err := cfg.Validator(c, user, pass)
			switch {
			case err != nil:
				return err
			case !ok:
				return challengeWith(c, challenge, errInvalidCredentials)
			}
			return next(c)
		}
	}
}

func challengeWith(c router.Context, challenge string, err error) error {
	c.Response().Header().Set(router.HeaderWWWAuthenticate, challenge)
	return err
}
