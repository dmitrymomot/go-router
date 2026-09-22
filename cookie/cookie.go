// Package cookie holds signed cookies and flash messages for go-router.
//
// A [Codec] signs a cookie value with HMAC-SHA256 and verifies it on the way
// back. The router holds no codec: the app builds one with [NewCodec] and
// keeps the *Codec in its own context struct, set by the context factory, as
// in
//
//	type Context struct {
//		router.Base
//		Cookies *cookie.Codec
//	}
//
// and a handler calls c.Cookies.Set, c.Cookies.Get, c.Cookies.AddFlash and
// c.Cookies.Flashes with its context.
package cookie

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

	"github.com/dmitrymomot/go-router"
)

// MinKeyLen is the shortest key that [NewCodec] takes.
const MinKeyLen = 32

// MaxSize is the largest Set-Cookie line a browser is required to keep.
const MaxSize = 4096

// DefaultMaxAge is how long a signed value stays valid when the cookie gives
// no lifetime of its own.
const DefaultMaxAge = 24 * time.Hour

var (
	// ErrInvalid reports a signed cookie that is malformed or that no key
	// verifies.
	ErrInvalid = errors.New("cookie: the signed cookie does not verify")

	// ErrExpired reports a signed cookie that verifies and has run out. It is
	// told apart from ErrInvalid, so a handler can log the first and simply
	// sign the user out on the second.
	ErrExpired = errors.New("cookie: the signed cookie expired")

	// ErrTooLarge reports a cookie whose Set-Cookie line exceeds [MaxSize]
	// once signed. Nothing is written, and the caller has to shorten the
	// value or drop a message.
	ErrTooLarge = errors.New("cookie: the cookie does not fit in 4096 bytes")
)

const sep = '.'

var enc = base64.RawURLEncoding

// Codec signs a cookie value with HMAC-SHA256, so a client can hold it and
// cannot change it. The value is signed and not encrypted: the client reads
// it.
//
// It signs with its current key and verifies with it and then with each
// previous key, so a key rotates without signing anyone out.
//
// [NewCodec] builds the only usable Codec: every method of the zero Codec
// panics. A Codec is safe for concurrent use.
type Codec struct {
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

// NewCodec builds a codec that signs with key and also accepts a value signed
// with any of previous, tried in order. It copies every key. Keep the keys out
// of the source and out of the repository.
//
// To rotate, build NewCodec(newKey, oldKey). Drop oldKey once every value it
// signed has run out: wait for the longest lifetime you sign with, whether the
// MaxAge or Expires of a cookie or [DefaultMaxAge].
//
// NewCodec panics on any key shorter than [MinKeyLen] bytes.
func NewCodec(key []byte, previous ...[]byte) *Codec {
	if len(key) < MinKeyLen {
		panic("cookie: NewCodec needs a key of at least " +
			strconv.Itoa(MinKeyLen) + " bytes, and got " + strconv.Itoa(len(key)))
	}
	for i, k := range previous {
		if len(k) < MinKeyLen {
			panic("cookie: NewCodec needs every previous key to be at least " +
				strconv.Itoa(MinKeyLen) + " bytes, and previous[" + strconv.Itoa(i) +
				"] has " + strconv.Itoa(len(k)))
		}
	}
	keys := make([]*codecKey, 0, 1+len(previous))
	keys = append(keys, newCodecKey(key))
	for _, k := range previous {
		keys = append(keys, newCodecKey(k))
	}
	return &Codec{keys: keys}
}

// mustBeBuilt panics on a Codec that NewCodec did not build, which has no key
// to sign with.
func (cc *Codec) mustBeBuilt() {
	if cc == nil || len(cc.keys) == 0 {
		panic("cookie: use NewCodec to build a Codec")
	}
}

// Encode signs value for a cookie called name and reports the string to store.
// The signature runs out at expires. The name is signed too, so a value cannot
// be moved to another cookie.
func (cc *Codec) Encode(name string, value []byte, expires time.Time) string {
	cc.mustBeBuilt()
	expiry := expires.Unix()
	sig := cc.keys[0].sign(name, expiry, value)
	buf := make([]byte, 0, enc.EncodedLen(len(value))+enc.EncodedLen(len(sig))+22)
	buf = enc.AppendEncode(buf, value)
	buf = append(buf, sep)
	buf = strconv.AppendInt(buf, expiry, 10)
	buf = append(buf, sep)
	buf = enc.AppendEncode(buf, sig)
	return string(buf)
}

// Decode verifies signed for a cookie called name and reports the value. It
// reports [ErrInvalid] when the string is malformed or no key verifies it, and
// [ErrExpired] when it has run out.
func (cc *Codec) Decode(name, signed string) ([]byte, error) {
	cc.mustBeBuilt()
	encValue, rest, ok := strings.Cut(signed, string(sep))
	if !ok {
		return nil, ErrInvalid
	}
	encExpiry, encSig, ok := strings.Cut(rest, string(sep))
	if !ok {
		return nil, ErrInvalid
	}
	value, err := enc.DecodeString(encValue)
	if err != nil {
		return nil, ErrInvalid
	}
	expiry, err := strconv.ParseInt(encExpiry, 10, 64)
	if err != nil {
		return nil, ErrInvalid
	}
	sig, err := enc.DecodeString(encSig)
	if err != nil {
		return nil, ErrInvalid
	}
	for _, k := range cc.keys {
		if subtle.ConstantTimeCompare(sig, k.sign(name, expiry, value)) != 1 {
			continue
		}
		if !time.Now().Before(time.Unix(expiry, 0)) {
			return nil, ErrExpired
		}
		return value, nil
	}
	return nil, ErrInvalid
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

// expiryOf reports when the signature of ck runs out: with the cookie, by its
// MaxAge first, then its Expires, and otherwise after DefaultMaxAge.
func expiryOf(ck *http.Cookie, now time.Time) time.Time {
	switch {
	case ck.MaxAge > 0:
		return now.Add(time.Duration(ck.MaxAge) * time.Second)
	case ck.MaxAge == 0 && !ck.Expires.IsZero():
		return ck.Expires
	default:
		return now.Add(DefaultMaxAge)
	}
}

// Set writes ck to the response of c with its value signed. Every other field
// of ck goes out as it stands, so the caller owns Path, Secure, HttpOnly and
// SameSite. Build ck with [router.Base.NewCookie] for the router's defaults.
// ck itself is left unchanged.
//
// The signature runs out with the cookie: MaxAge first, then Expires, and
// otherwise after [DefaultMaxAge].
//
// It reports [ErrTooLarge], and writes nothing, when the signed Set-Cookie
// line is longer than [MaxSize]. A cookie whose name is not a valid token
// writes nothing, as with [http.SetCookie].
func (cc *Codec) Set(c router.Context, ck *http.Cookie) error {
	cc.mustBeBuilt()
	signed := *ck
	signed.Value = cc.Encode(ck.Name, []byte(ck.Value), expiryOf(ck, time.Now()))
	line := signed.String()
	if len(line) > MaxSize {
		return ErrTooLarge
	}
	if line != "" {
		c.Response().Header().Add(router.HeaderSetCookie, line)
	}
	return nil
}

// Get reads and verifies the cookie called name from the request of c. It
// reports [http.ErrNoCookie] when the request carries no such cookie, and
// otherwise the failure of [Codec.Decode].
//
// A client that sends the name more than once, which happens across
// subdomains, has each copy tried and the first that verifies wins.
func (cc *Codec) Get(c router.Context, name string) (string, error) {
	cc.mustBeBuilt()
	cookies := c.Request().CookiesNamed(name)
	if len(cookies) == 0 {
		return "", http.ErrNoCookie
	}
	var first error
	for _, ck := range cookies {
		value, err := cc.Decode(name, ck.Value)
		if err == nil {
			return string(value), nil
		}
		if first == nil {
			first = err
		}
	}
	return "", first
}

// newCookie builds a cookie with the router's defaults, as
// [router.Base.NewCookie] does.
func newCookie(c router.Context, name, value string, maxAge time.Duration) *http.Cookie {
	b, ok := router.FromContext(c)
	if !ok {
		b = router.NewBase(c.Response(), c.Request())
	}
	return b.NewCookie(name, value, maxAge)
}
