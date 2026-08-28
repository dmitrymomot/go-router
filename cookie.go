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

const MinCookieKeyLen = 32

const MaxCookieSize = 4096

const DefaultCookieMaxAge = 24 * time.Hour

var (
	ErrCookieInvalid = errors.New("router: the signed cookie does not verify")
	ErrCookieExpired = errors.New("router: the signed cookie expired")
)

const cookieSep = '.'

var cookieEnc = base64.RawURLEncoding

type CookieCodec struct {
	MaxAge time.Duration

	key []byte
}

func NewCookieCodec(key []byte) *CookieCodec {
	if len(key) < MinCookieKeyLen {
		panic("router: NewCookieCodec needs a key of at least " +
			strconv.Itoa(MinCookieKeyLen) + " bytes, and got " + strconv.Itoa(len(key)))
	}
	return &CookieCodec{MaxAge: DefaultCookieMaxAge, key: bytes.Clone(key)}
}

func (cc *CookieCodec) maxAge() time.Duration {
	if cc.MaxAge <= 0 {
		return DefaultCookieMaxAge
	}
	return cc.MaxAge
}

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

	mac := hmac.New(sha256.New, cc.key)
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

func (b *Base) SetSignedCookie(cc *CookieCodec, c *http.Cookie) {
	signed := *c
	signed.Value = cc.encode(c.Name, []byte(c.Value), signedExpiry(cc, c, time.Now()))
	http.SetCookie(b.res, &signed)
}

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
