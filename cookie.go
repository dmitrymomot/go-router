package router

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MinCookieKeyLen is the shortest signing key that [NewCookieCodec] accepts,
// in bytes. It is the output size of SHA-256, which is the size that HMAC
// gains nothing beyond.
const MinCookieKeyLen = 32

// MaxCookieSize is the size that a browser guarantees for one cookie, in
// bytes, counting the name, the value and the attributes together. A browser
// also caps how many cookies one domain holds, so a cookie that fits is not a
// cookie that arrives.
const MaxCookieSize = 4096

// DefaultCookieMaxAge is how long a signature stays valid when neither the
// codec nor the cookie names a lifetime.
const DefaultCookieMaxAge = 24 * time.Hour

// The failures that [CookieCodec.Decode] reports.
var (
	// ErrCookieInvalid means that the value is malformed, that it carries
	// another name, that another key signed it, or that someone changed it.
	// The four are one answer on purpose, because the client learns nothing
	// from which of them it is.
	ErrCookieInvalid = errors.New("router: the signed cookie does not verify")

	// ErrCookieExpired means that the signature verifies and that its expiry
	// passed. The value came from this server, so a handler that re-issues a
	// cookie acts on it.
	ErrCookieExpired = errors.New("router: the signed cookie expired")
)

// cookieSep separates the fields of a signed value. Base64url uses none of it,
// so a field never carries one, and RFC 6265 lets a cookie value hold it.
const cookieSep = '.'

// cookieEnc is the alphabet of a signed value. It is unpadded, because "=" is
// what separates a cookie name from its value.
var cookieEnc = base64.RawURLEncoding

// CookieCodec signs and verifies a cookie value with HMAC-SHA256. The value
// stays readable by the client; the signature only proves that the server
// wrote it. Encrypt anything that the client must not read before it reaches
// the codec, or keep it on the server and put an identifier in the cookie.
//
// The signature covers the cookie name and an expiry as well as the value. A
// value therefore verifies under the name that signed it and under no other,
// which keeps a value from moving out of one cookie and into another, and a
// signature stops verifying once its expiry passes.
//
// One codec serves every request. Build it at start-up, set MaxAge before the
// server starts, and read it from there.
type CookieCodec struct {
	// MaxAge is how long a signature that [CookieCodec.Encode] writes stays
	// valid. Zero and less mean [DefaultCookieMaxAge].
	//
	// [Base.SetSignedCookie] takes the lifetime of the cookie instead when the
	// cookie names one, and falls back to this.
	MaxAge time.Duration

	key []byte
}

// NewCookieCodec returns a codec that signs with key. It copies the key, so a
// caller that zeroes the original changes nothing here.
//
// It panics on a key shorter than [MinCookieKeyLen] bytes. A short key is
// almost always a password that reached the argument in place of a key, and a
// panic at start-up names that where a weak signature never does.
//
// Draw the key from crypto/rand once and keep it in the environment or in a
// secret store. Every restart that draws a new one invalidates every signature
// that the old one wrote, which logs out everyone holding a cookie.
func NewCookieCodec(key []byte) *CookieCodec {
	if len(key) < MinCookieKeyLen {
		panic("router: NewCookieCodec needs a key of at least " +
			strconv.Itoa(MinCookieKeyLen) + " bytes, and got " + strconv.Itoa(len(key)))
	}
	return &CookieCodec{MaxAge: DefaultCookieMaxAge, key: bytes.Clone(key)}
}

// maxAge is the lifetime that the codec applies, with the default in place of
// a lifetime that a caller left unset.
func (cc *CookieCodec) maxAge() time.Duration {
	if cc.MaxAge <= 0 {
		return DefaultCookieMaxAge
	}
	return cc.MaxAge
}

// Encode returns the signed form of value, for a cookie named name. The result
// is base64 and two separators, so it goes into a cookie value as it stands.
//
// The signature expires MaxAge from now. Write the cookie with
// [Base.SetSignedCookie], which keeps that expiry and the lifetime of the
// cookie itself in step.
func (cc *CookieCodec) Encode(name string, value []byte) string {
	return cc.encode(name, value, time.Now().Add(cc.maxAge()).Unix())
}

// encode signs value with an expiry that the caller settled.
func (cc *CookieCodec) encode(name string, value []byte, expiry int64) string {
	sig := cc.sign(name, expiry, value)
	buf := make([]byte, 0, cookieEnc.EncodedLen(len(value))+cookieEnc.EncodedLen(len(sig))+22)
	buf = cookieEnc.AppendEncode(buf, value)
	buf = append(buf, cookieSep)
	buf = strconv.AppendInt(buf, expiry, 10)
	buf = append(buf, cookieSep)
	buf = cookieEnc.AppendEncode(buf, sig)
	return string(buf)
}

// Decode returns the value that [CookieCodec.Encode] signed for a cookie named
// name.
//
// It reports [ErrCookieInvalid] for a value that does not verify and
// [ErrCookieExpired] for one that verifies past its expiry. It checks the
// signature before the expiry, so an expired answer names a value that this
// server wrote and not one that a client made up.
func (cc *CookieCodec) Decode(name string, signed string) ([]byte, error) {
	encValue, rest, ok := strings.Cut(signed, string(cookieSep))
	if !ok {
		return nil, ErrCookieInvalid
	}
	encExpiry, encSig, ok := strings.Cut(rest, string(cookieSep))
	if !ok {
		return nil, ErrCookieInvalid
	}
	value, err := cookieEnc.DecodeString(encValue)
	if err != nil {
		return nil, ErrCookieInvalid
	}
	expiry, err := strconv.ParseInt(encExpiry, 10, 64)
	if err != nil {
		return nil, ErrCookieInvalid
	}
	sig, err := cookieEnc.DecodeString(encSig)
	if err != nil {
		return nil, ErrCookieInvalid
	}
	if subtle.ConstantTimeCompare(sig, cc.sign(name, expiry, value)) != 1 {
		return nil, ErrCookieInvalid
	}
	if !time.Now().Before(time.Unix(expiry, 0)) {
		return nil, ErrCookieExpired
	}
	return value, nil
}

// sign returns the HMAC over the name, the expiry and the value.
//
// The length of the name comes first, so that every input maps to one string
// of bytes and no other. Without it, a name that ends where a value begins
// signs the same bytes as a shorter name and a longer value, and a value moves
// between the two cookies.
//
//nolint:errcheck // hash.Hash.Write never returns an error.
func (cc *CookieCodec) sign(name string, expiry int64, value []byte) []byte {
	var header [16]byte
	binary.BigEndian.PutUint64(header[:8], uint64(len(name)))
	binary.BigEndian.PutUint64(header[8:], uint64(expiry))

	mac := hmac.New(sha256.New, cc.key)
	mac.Write(header[:])
	mac.Write([]byte(name))
	mac.Write(value)
	return mac.Sum(nil)
}

// signedExpiry returns the moment at which the signature of c stops
// verifying, as a Unix time.
func signedExpiry(cc *CookieCodec, c *http.Cookie, now time.Time) int64 {
	switch {
	case c.MaxAge > 0:
		return now.Add(time.Duration(c.MaxAge) * time.Second).Unix()
	case c.MaxAge == 0 && !c.Expires.IsZero():
		return c.Expires.Unix()
	default:
		return now.Add(cc.maxAge()).Unix()
	}
}

// SetSignedCookie signs the value of c and writes the result as a Set-Cookie
// header. It leaves c alone, so the caller keeps the plain value:
//
//	c.SetSignedCookie(codec, &http.Cookie{
//		Name:     "uid",
//		Value:    user.ID,
//		Path:     "/",
//		MaxAge:   int(7 * 24 * time.Hour / time.Second),
//		Secure:   true,
//		HttpOnly: true,
//		SameSite: http.SameSiteLaxMode,
//	})
//
// The signature expires when the cookie does: MaxAge counts from now, Expires
// names the moment, and a cookie that sets neither lasts for the browser
// session and for [CookieCodec.MaxAge] in the signature. The two match on
// purpose. A signature that dies first leaves the browser sending a cookie
// that stopped verifying, which reads to the user as a session that ended for
// no reason; one that outlives its cookie keeps a value good past the moment
// the server named.
func (b *Base) SetSignedCookie(cc *CookieCodec, c *http.Cookie) {
	signed := *c
	signed.Value = cc.encode(c.Name, []byte(c.Value), signedExpiry(cc, c, time.Now()))
	http.SetCookie(b.res, &signed)
}

// SignedCookie returns the value of the named signed cookie. It reports
// [http.ErrNoCookie] when the request carries none, and otherwise the failure
// that [CookieCodec.Decode] reported:
//
//	id, err := c.SignedCookie(codec, "uid")
//	if err != nil {
//		return c.Redirect(http.StatusSeeOther, "/login")
//	}
//
// A request that carries two cookies of the name takes the first that
// verifies. A page on a sibling subdomain sets a cookie that the browser sends
// next to this one, and it holds no key to sign one with, so the one that
// verifies is the one that this server wrote.
func (b *Base) SignedCookie(cc *CookieCodec, name string) ([]byte, error) {
	cookies := b.req.CookiesNamed(name)
	if len(cookies) == 0 {
		return nil, http.ErrNoCookie
	}
	var first error
	for _, c := range cookies {
		value, err := cc.Decode(name, c.Value)
		if err == nil {
			return value, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, first
}
