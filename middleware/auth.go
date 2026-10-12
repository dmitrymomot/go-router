package middleware

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// DefaultRealm is the realm that [BasicAuth] names in its challenge.
const DefaultRealm = "Restricted"

const basicScheme = "Basic"

var (
	errMissingKey         = router.ErrUnauthorized.WithMessage("missing key")
	errInvalidKey         = router.ErrUnauthorized.WithMessage("invalid key")
	errMissingCredentials = router.ErrUnauthorized.WithMessage("missing credentials")
	errInvalidCredentials = router.ErrUnauthorized.WithMessage("invalid credentials")
	errMalformedBasicAuth = router.ErrBadRequest.WithMessage("malformed credentials")
)

// SecureCompare reports whether a and b are equal, in time that does not
// depend on where they differ. Use it to compare a secret, so a caller cannot
// learn it one byte at a time.
//
// The length of a and b still leaks, which is fine for a token of fixed
// length.
func SecureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// KeyAuthConfig configures [KeyAuthWithConfig].
//
// Validator says whether a key is good, and it is required; it may reach the
// database through the fields of C. Sources say where the key comes from, and
// an empty list reads a Bearer token from Authorization.
//
// Challenge is the WWW-Authenticate value of a 401, and an empty one sends
// "Bearer".
//
// OnReject answers a refused request. It gets the failure: a 401 for a missing
// or bad key, or the error of Validator. Return an error to report it in place
// of the request, or nil to say that OnReject wrote the answer itself. A nil
// OnReject reports the failure. ContinueOnNilReject lets the request through
// when OnReject reports nil, which suits an optional sign-in. The challenge
// goes on a 401 that OnReject returns as an error, and an OnReject that writes
// a 401 itself sets its own.
type KeyAuthConfig[C router.Context] struct {
	Skip                func(c C) bool
	Sources             []TokenSource
	Validator           func(c C, key string) (bool, error)
	OnReject            func(c C, err error) error
	ContinueOnNilReject bool
	Challenge           string
}

var defaultKeyAuthSources = []TokenSource{FromHeader(router.HeaderAuthorization, "Bearer ")}

// KeyAuth refuses a request whose API key v does not accept. It reads a Bearer
// token from the Authorization header and answers 401 with a challenge.
//
// v checks one key per request: the first that the first source holding one
// offers. A later key, from a repeated header or another source, is never
// tried, so a request whose first key is wrong is refused even when a good one
// follows. An error from v is reported as it stands, so a database failure
// reaches the client as a 500 and not as a 401.
//
// See Order in the package doc for where it goes.
//
// KeyAuth panics if v is nil.
func KeyAuth[C router.Context](v func(c C, key string) (bool, error)) router.Middleware[C] {
	return KeyAuthWithConfig(KeyAuthConfig[C]{Validator: v})
}

// KeyAuthWithConfig is [KeyAuth] with a configuration.
//
// KeyAuthWithConfig panics on a nil Validator, a nil token source, and more
// than [MaxTokenSources] of them.
func KeyAuthWithConfig[C router.Context](cfg KeyAuthConfig[C]) router.Middleware[C] {
	if cfg.Validator == nil {
		panic("middleware: KeyAuthConfig needs a Validator")
	}
	if len(cfg.Sources) == 0 {
		cfg.Sources = defaultKeyAuthSources
	}
	if cfg.Challenge == "" {
		cfg.Challenge = "Bearer"
	}
	cfg.Sources = slices.Clone(cfg.Sources)
	checkTokenSources("KeyAuthConfig", cfg.Sources)

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			var key string
			for token := range readTokens(c, cfg.Sources) {
				key = token
				break
			}

			failure := error(errMissingKey)
			if key != "" {
				ok, err := cfg.Validator(c, key)
				switch {
				case err != nil:
					failure = err
				case ok:
					return next(c)
				default:
					failure = errInvalidKey
				}
			}

			if cfg.OnReject == nil {
				return keyAuthChallenge(c.Response(), cfg.Challenge, failure)
			}
			if err := cfg.OnReject(c, failure); err != nil {
				return keyAuthChallenge(c.Response(), cfg.Challenge, err)
			}
			if cfg.ContinueOnNilReject {
				return next(c)
			}
			return nil
		}
	}
}

// keyAuthChallenge sets the challenge on a 401 that has none and reports err.
func keyAuthChallenge(res *router.Response, challenge string, err error) error {
	if router.StatusOf(err) == http.StatusUnauthorized && !res.Committed &&
		res.Header().Get(router.HeaderWWWAuthenticate) == "" {
		res.Header().Set(router.HeaderWWWAuthenticate, challenge)
	}
	return err
}

// BasicAuthConfig configures [BasicAuthWithConfig]. Validator is required, and
// an empty Realm takes [DefaultRealm].
type BasicAuthConfig[C router.Context] struct {
	Skip      func(c C) bool
	Validator func(c C, user, pass string) (bool, error)
	Realm     string
}

// BasicAuth refuses a request whose credentials v does not accept, and answers
// 401 with a challenge that names [DefaultRealm]. Credentials travel in the
// clear, so use it over HTTPS alone.
//
// A validator that compares a password has to do so in constant time; see
// [SecureCompare]. An Authorization header this middleware cannot decode is a
// 400 and v never runs.
//
// See Order in the package doc for where it goes.
//
// BasicAuth panics if v is nil.
func BasicAuth[C router.Context](v func(c C, user, pass string) (bool, error)) router.Middleware[C] {
	return BasicAuthWithConfig(BasicAuthConfig[C]{Validator: v})
}

// BasicAuthWithConfig is [BasicAuth] with a configuration.
//
// BasicAuthWithConfig panics on a nil Validator.
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
