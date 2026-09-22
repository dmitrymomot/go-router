package router

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MinCookieKeyLen is the shortest key that [NewCookieCodec] takes.
const MinCookieKeyLen = 32

// MaxCookieSize is the largest Set-Cookie line a browser is required to keep.
const MaxCookieSize = 4096

// DefaultCookieMaxAge is how long a signed cookie stays valid when neither the
// codec nor the cookie says.
const DefaultCookieMaxAge = 24 * time.Hour

// The failures that [CookieCodec.Decode] reports. A cookie that does not
// verify and one that has run out are told apart, so a handler can log the
// first and simply sign the user out on the second.
var (
	ErrCookieInvalid = errors.New("router: the signed cookie does not verify")
	ErrCookieExpired = errors.New("router: the signed cookie expired")
)

const cookieSep = '.'

var cookieEnc = base64.RawURLEncoding

// CookieCodec signs a cookie value with HMAC-SHA256, so a client can hold it
// and cannot change it. The value is signed and not encrypted: the client
// reads it.
//
// It signs with its current key and verifies with it and then with each
// previous key, so a key rotates without signing anyone out.
//
// MaxAge is how long a value stays valid; zero or less takes
// [DefaultCookieMaxAge]. A codec is safe for concurrent use.
type CookieCodec struct {
	MaxAge time.Duration
	// keys[0] signs, and every key verifies, in order. Pointers, because a
	// sync.Pool must not be copied.
	keys []*codecKey
}

type codecKey struct {
	key []byte
	// hmac.New builds and keys two hashes per call, and a flash is signed or
	// verified at least twice per request.
	macs sync.Pool
}

func newCodecKey(key []byte) *codecKey {
	k := &codecKey{key: bytes.Clone(key)}
	k.macs.New = func() any { return hmac.New(sha256.New, k.key) }
	return k
}

// NewCookieCodec builds a codec that signs with key and also accepts a value
// signed with any of previous, tried in order. It copies every key. Keep the
// keys out of the source and out of the repository.
//
// To rotate, build NewCookieCodec(newKey, oldKey). Drop oldKey once every
// value it signed has run out: wait for the longest lifetime you sign with,
// whether the MaxAge or Expires of a cookie or the MaxAge of the codec.
//
// NewCookieCodec panics on any key shorter than [MinCookieKeyLen] bytes.
func NewCookieCodec(key []byte, previous ...[]byte) *CookieCodec {
	if len(key) < MinCookieKeyLen {
		panic("router: NewCookieCodec needs a key of at least " +
			strconv.Itoa(MinCookieKeyLen) + " bytes, and got " + strconv.Itoa(len(key)))
	}
	for i, k := range previous {
		if len(k) < MinCookieKeyLen {
			panic("router: NewCookieCodec needs every previous key to be at least " +
				strconv.Itoa(MinCookieKeyLen) + " bytes, and previous[" + strconv.Itoa(i) +
				"] has " + strconv.Itoa(len(k)))
		}
	}
	keys := make([]*codecKey, 0, 1+len(previous))
	keys = append(keys, newCodecKey(key))
	for _, k := range previous {
		keys = append(keys, newCodecKey(k))
	}
	return &CookieCodec{MaxAge: DefaultCookieMaxAge, keys: keys}
}

func (cc *CookieCodec) maxAge() time.Duration {
	if cc.MaxAge <= 0 {
		return DefaultCookieMaxAge
	}
	return cc.MaxAge
}

// Encode signs value for a cookie called name and reports the string to store.
// The name is signed too, so a value cannot be moved to another cookie.
func (cc *CookieCodec) Encode(name string, value []byte) string {
	return cc.encode(name, value, time.Now().Add(cc.maxAge()).Unix())
}

func (cc *CookieCodec) encode(name string, value []byte, expiry int64) string {
	sig := cc.keys[0].sign(name, expiry, value)
	buf := make([]byte, 0, cookieEnc.EncodedLen(len(value))+cookieEnc.EncodedLen(len(sig))+22)
	buf = cookieEnc.AppendEncode(buf, value)
	buf = append(buf, cookieSep)
	buf = strconv.AppendInt(buf, expiry, 10)
	buf = append(buf, cookieSep)
	buf = cookieEnc.AppendEncode(buf, sig)
	return string(buf)
}

// Decode verifies signed for a cookie called name and reports the value. It
// reports [ErrCookieInvalid] when the string is malformed or no key verifies
// it, and [ErrCookieExpired] when it has run out.
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
	for _, k := range cc.keys {
		if subtle.ConstantTimeCompare(sig, k.sign(name, expiry, value)) != 1 {
			continue
		}
		if !time.Now().Before(time.Unix(expiry, 0)) {
			return nil, ErrCookieExpired
		}
		return value, nil
	}
	return nil, ErrCookieInvalid
}

// The name length comes first, so a shorter name with a longer value cannot
// sign the same bytes and move a value between two cookies.
//
//nolint:errcheck // hash.Hash.Write never returns an error.
func (k *codecKey) sign(name string, expiry int64, value []byte) []byte {
	var header [16]byte
	binary.BigEndian.PutUint64(header[:8], uint64(len(name)))
	binary.BigEndian.PutUint64(header[8:], uint64(expiry))

	mac := k.macs.Get().(hash.Hash)
	defer func() {
		mac.Reset()
		k.macs.Put(mac)
	}()
	mac.Write(header[:])
	mac.Write([]byte(name))
	mac.Write(value)
	return mac.Sum(nil)
}

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

// Cookie reports the value of the cookie called name, or "" when the request
// carries none or carries it empty. A request that sends the name twice has
// the first one read. See [Base.SignedCookie] for a cookie the client cannot
// forge.
func (b *Base) Cookie(name string) string {
	c, err := b.req.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// NewCookie builds a cookie the way the router builds its own: Path "/",
// HttpOnly, SameSite=Lax, and Secure when [Base.Scheme] reports https. Change
// any field before [Base.SetCookie] or [Base.SetSignedCookie] writes it.
//
// maxAge is how long the browser keeps the cookie, rounded up to whole
// seconds. Zero makes a session cookie, which lasts until the browser closes,
// and a negative maxAge makes one that deletes the cookie at once.
//
// The value goes out as net/http writes it, which drops a semicolon, a quote,
// a backslash or a control byte. Escape a value that may hold one.
func (b *Base) NewCookie(name, value string, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   cookieMaxAge(maxAge),
		Secure:   b.Scheme() == "https",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// A sub-second lifetime rounds up, so it never turns into a session cookie.
func cookieMaxAge(d time.Duration) int {
	switch {
	case d < 0:
		return -1
	case d%time.Second != 0:
		return int(d/time.Second) + 1
	default:
		return int(d / time.Second)
	}
}

// SetCookie adds a Set-Cookie header to the response. [Base.NewCookie] builds
// one with the router's defaults. A cookie whose name is not a valid token
// writes nothing, as with [http.SetCookie].
func (b *Base) SetCookie(c *http.Cookie) { http.SetCookie(b.res, c) }

// ClearCookie tells the browser to drop the cookie called name, as
// [Base.NewCookie] built it. It writes whether or not the request carries the
// cookie. A cookie set with another Path or with a Domain is dropped only by
// one with the same Path and Domain: take NewCookie(name, "", -1) and set them.
func (b *Base) ClearCookie(name string) { b.SetCookie(b.NewCookie(name, "", -1)) }

// SetSignedCookie writes c with its value signed by cc. Every other field of c
// goes out as it stands, so the caller owns Path, Secure, HttpOnly and
// SameSite. Build c with [Base.NewCookie] for the router's defaults.
//
// The signature runs out with the cookie: MaxAge first, then Expires, then the
// MaxAge of the codec.
func (b *Base) SetSignedCookie(cc *CookieCodec, c *http.Cookie) {
	signed := *c
	signed.Value = cc.encode(c.Name, []byte(c.Value), signedExpiry(cc, c, time.Now()))
	http.SetCookie(b.res, &signed)
}

// SignedCookie reads and verifies the cookie called name. It reports
// [http.ErrNoCookie] when the request carries none, and otherwise the failure
// of [CookieCodec.Decode].
//
// A client that sends the name more than once, which happens across
// subdomains, has each copy tried and the first that verifies wins.
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
