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
// MaxAge is how long a value stays valid; zero or less takes
// [DefaultCookieMaxAge]. A codec is safe for concurrent use.
type CookieCodec struct {
	MaxAge time.Duration
	key    []byte
	// hmac.New builds and keys two hashes per call, and a flash is signed or
	// verified at least twice per request.
	macs sync.Pool
}

// NewCookieCodec builds a codec that signs with key, which it copies. Keep the
// key out of the source and out of the repository.
//
// NewCookieCodec panics on a key shorter than [MinCookieKeyLen] bytes.
func NewCookieCodec(key []byte) *CookieCodec {
	if len(key) < MinCookieKeyLen {
		panic("router: NewCookieCodec needs a key of at least " +
			strconv.Itoa(MinCookieKeyLen) + " bytes, and got " + strconv.Itoa(len(key)))
	}
	cc := &CookieCodec{MaxAge: DefaultCookieMaxAge, key: bytes.Clone(key)}
	cc.macs.New = func() any { return hmac.New(sha256.New, cc.key) }
	return cc
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
	sig := cc.sign(name, expiry, value)
	buf := make([]byte, 0, cookieEnc.EncodedLen(len(value))+cookieEnc.EncodedLen(len(sig))+22)
	buf = cookieEnc.AppendEncode(buf, value)
	buf = append(buf, cookieSep)
	buf = strconv.AppendInt(buf, expiry, 10)
	buf = append(buf, cookieSep)
	buf = cookieEnc.AppendEncode(buf, sig)
	return string(buf)
}

// Decode verifies signed for a cookie called name and reports the value. It
// reports [ErrCookieInvalid] when the string is malformed or does not verify,
// and [ErrCookieExpired] when it has run out.
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

// The name length comes first, so a shorter name with a longer value cannot
// sign the same bytes and move a value between two cookies.
//
//nolint:errcheck // hash.Hash.Write never returns an error.
func (cc *CookieCodec) sign(name string, expiry int64, value []byte) []byte {
	var header [16]byte
	binary.BigEndian.PutUint64(header[:8], uint64(len(name)))
	binary.BigEndian.PutUint64(header[8:], uint64(expiry))

	mac := cc.macs.Get().(hash.Hash)
	defer func() {
		mac.Reset()
		cc.macs.Put(mac)
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

// SetSignedCookie writes c with its value signed by cc. Every other field of c
// goes out as it stands, so the caller owns Path, Secure, HttpOnly and
// SameSite.
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
