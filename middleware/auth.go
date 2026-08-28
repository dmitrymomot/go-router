package middleware

import (
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// DefaultRealm is the protection space that [BasicAuth] names in its
// challenge.
const DefaultRealm = "Restricted"

// basicScheme is the authentication scheme of RFC 7617.
const basicScheme = "Basic"

// The failures that [KeyAuthWithConfig] and [BasicAuthWithConfig] report. They
// reach [KeyAuthConfig.OnError] as they are, so a config tells a key that the
// request never sent from one that the store does not know.
var (
	errMissingKey         = router.ErrUnauthorized.WithMessage("missing key")
	errInvalidKey         = router.ErrUnauthorized.WithMessage("invalid key")
	errMissingCredentials = router.ErrUnauthorized.WithMessage("missing credentials")
	errInvalidCredentials = router.ErrUnauthorized.WithMessage("invalid credentials")
	errMalformedBasicAuth = router.ErrBadRequest.WithMessage("malformed credentials")
)

// SecureCompare reports whether a and b are equal, in time that does not
// depend on where they first differ.
//
// A comparison that returns at the first byte that differs tells the caller
// how much of a guess was right, which turns a search of the whole key space
// into a search of one byte at a time. Compare a secret with this and never
// with ==:
//
//	func check(c *app.Context, key string) (bool, error) {
//		return middleware.SecureCompare(key, c.Config.APIKey), nil
//	}
//
// It hides where two values differ, not how long they are: values of different
// length are never equal, and the answer for those comes back at once. A key
// that the application looks up in a store, or a password that it puts through
// a hash verifier, needs neither this nor any other guard.
func SecureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// KeyAuthConfig configures [KeyAuthWithConfig]. C is the context type of the
// router, so the validator writes the caller that it identified onto the
// context and the handler reads a typed field instead of asserting one out of
// a store.
type KeyAuthConfig[C router.Context] struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Sources are the places that carry the key, tried in order. They default
	// to the Authorization header with the Bearer scheme.
	Sources []TokenSource

	// Validator reports whether the key names a caller. It runs on the
	// application context, so it puts that caller on a field of it:
	//
	//	func(c *app.Context, key string) (bool, error) {
	//		caller, err := app.Callers.ByKey(c, key)
	//		if errors.Is(err, app.ErrNoCaller) {
	//			return false, nil
	//		}
	//		if err != nil {
	//			return false, err
	//		}
	//		c.Caller = caller
	//		return true, nil
	//	}
	//
	// A false answer is a key that no caller holds, and the client sees a 401.
	// An error is a fault of the server, and the client sees the status of
	// that error.
	//
	// Compare a key that the application holds in memory with [SecureCompare].
	// A config without a validator panics at construction.
	Validator func(c C, key string) (bool, error)

	// OnError answers a request that no key authenticated. It returns an error
	// to answer with that error instead, and nil to report that it answered
	// the request itself.
	OnError func(c C, err error) error

	// ContinueOnIgnoredError passes a request that OnError returned nil for on
	// to the handler, unauthenticated. Turn it on for a route that the public
	// reads and that says more to a caller it knows.
	ContinueOnIgnoredError bool
}

// defaultKeyAuthSources is the source list of a config that names none.
var defaultKeyAuthSources = []TokenSource{FromHeader(router.HeaderAuthorization, "Bearer ")}

// KeyAuth is [KeyAuthWithConfig] with the validator and its default config,
// which reads a bearer token out of the Authorization header:
//
//	r.Use(middleware.KeyAuth(func(c Ctx, key string) (bool, error) {
//		caller, err := app.Callers.ByKey(c, key)
//		if err != nil {
//			return false, err
//		}
//		c.Caller = caller
//		return true, nil
//	}))
//
// The validator names the context type, so this one call needs no type
// argument of its own.
func KeyAuth[C router.Context](v func(c C, key string) (bool, error)) router.Middleware[C] {
	return KeyAuthWithConfig(KeyAuthConfig[C]{Validator: v})
}

// KeyAuthWithConfig authenticates a request by an API key that the validator
// checks.
//
// It hands the validator every key that the sources yield, at most
// [MaxTokensPerRequest] of them, and passes the request on at the first one
// that the validator accepts. A request that presents no key at all reports a
// 401 without calling the validator once.
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
					// A validator that failed reports a fault of the server,
					// which a later key that is merely wrong must not hide.
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

// BasicAuthConfig configures [BasicAuthWithConfig]. C is the context type of
// the router, so the validator writes the user that it identified onto the
// context and the handler reads a typed field.
type BasicAuthConfig[C router.Context] struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Validator reports whether the pair names a user. It runs on the
	// application context, so it puts that user on a field of it.
	//
	// Verify the password against a password hash. Compare one that the
	// application holds in memory with [SecureCompare], and compare the user
	// name with it too, so that the answer does not depend on how much of the
	// name was right.
	//
	// A config without a validator panics at construction.
	Validator func(c C, user, pass string) (bool, error)

	// Realm is the protection space that the challenge names, which is the
	// text that a browser shows above its prompt. It defaults to
	// [DefaultRealm].
	Realm string
}

// BasicAuth is [BasicAuthWithConfig] with the validator and its default
// config, whose realm is [DefaultRealm]:
//
//	r.Use(middleware.BasicAuth(func(c Ctx, user, pass string) (bool, error) {
//		return middleware.SecureCompare(user, "ops") &&
//			middleware.SecureCompare(pass, secret), nil
//	}))
//
// The validator names the context type, so this one call needs no type
// argument of its own.
func BasicAuth[C router.Context](v func(c C, user, pass string) (bool, error)) router.Middleware[C] {
	return BasicAuthWithConfig(BasicAuthConfig[C]{Validator: v})
}

// BasicAuthWithConfig authenticates a request by the user name and password of
// RFC 7617.
//
// A request carries one such credential, so the validator runs at most once
// per request. A 401 carries the WWW-Authenticate challenge that makes a
// browser prompt. A header that is not a credential at all, one whose base64
// does not decode or that holds no colon, reports a 400 instead: the client
// sent something malformed rather than something wrong, and a challenge would
// only make it send the same again.
//
// Basic authentication sends the password on every request, in an encoding
// that anyone on the way reads. Serve it over TLS alone.
func BasicAuthWithConfig[C router.Context](cfg BasicAuthConfig[C]) router.Middleware[C] {
	if cfg.Validator == nil {
		panic("middleware: BasicAuthConfig needs a Validator")
	}
	if cfg.Realm == "" {
		cfg.Realm = DefaultRealm
	}
	// The realm is a quoted string, and quoting it once here also keeps a
	// realm that carries a quote or a newline from forging a header of its own.
	challenge := basicScheme + " realm=" + strconv.Quote(cfg.Realm)

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			scheme, rest, found := strings.Cut(
				c.Request().Header.Get(router.HeaderAuthorization), " ")
			rest = strings.TrimSpace(rest)

			// RFC 7617 spells the scheme "Basic" and RFC 7235 lets the client
			// spell it any way at all.
			if !found || !strings.EqualFold(scheme, basicScheme) || rest == "" {
				return challengeWith(c, challenge, errMissingCredentials)
			}

			raw, err := base64.StdEncoding.DecodeString(rest)
			if err != nil {
				return errMalformedBasicAuth.WithError(err)
			}
			// The user name holds no colon, so the first one ends it and a
			// password that carries colons of its own survives whole.
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

// challengeWith puts the challenge on the response and returns the failure, so
// that every 401 of [BasicAuthWithConfig] carries one.
func challengeWith(c router.Context, challenge string, err error) error {
	c.Response().Header().Set(router.HeaderWWWAuthenticate, challenge)
	return err
}
