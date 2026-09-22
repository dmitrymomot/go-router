package router

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// FlashCookieName is the cookie that carries the flash messages.
const FlashCookieName = "_flash"

// FlashMaxAge is how long a flash message survives before the browser drops
// it.
const FlashMaxAge = 5 * time.Minute

const headerSetCookie = "Set-Cookie"

// ErrFlashTooLarge reports that the messages exceed [MaxCookieSize] once
// signed. The cookie is left as it was, so no message is lost, and the caller
// has to shorten or drop one.
var ErrFlashTooLarge = errors.New("router: the flash messages do not fit in one cookie")

// Flash is one message that survives a redirect. Kind is yours to choose,
// such as "error" or "success".
type Flash struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// AddFlash appends f to the flash cookie, which the codec of
// [Router.CookieCodec] signs. The cookie is the one [Base.NewCookie] builds:
// HttpOnly, SameSite=Lax, and Secure over HTTPS.
//
// The message travels with whatever this response is: a redirect, an
// HX-Redirect or a page. [Base.Flashes] in the same request reads it back, so
// an htmx partial can show it in place.
//
// It reports [ErrNoCookieCodec] when the router has no codec, and
// [ErrFlashTooLarge] when the messages no longer fit.
func (b *Base) AddFlash(f Flash) error {
	cc := b.codec()
	if cc == nil {
		return ErrNoCookieCodec
	}
	flashes := b.flashes(cc)
	flashes = append(flashes, f)

	data, err := json.Marshal(flashes)
	if err != nil {
		return fmt.Errorf("router: encode the flash messages: %w", err)
	}

	b.Vary(HeaderCookie)

	c := b.NewCookie(FlashCookieName, "", FlashMaxAge)
	c.Value = cc.encode(c.Name, data, signedExpiry(cc, c, time.Now()))
	if len(c.String()) > MaxCookieSize {
		return ErrFlashTooLarge
	}
	b.writeFlashCookie(c)
	return nil
}

// Flashes reports the messages the request carried, then those this response
// added, and clears them, so each is shown once. A second call in the same
// request reports nothing. Add first, then read: a layout that calls Flashes
// sees what the handler added before it.
//
// Clearing is a header, so call Flashes before the response is committed.
// [Base.Render] buffers, so a template it runs may call Flashes; one that
// [Base.RenderStream] runs may not.
//
// It reports nothing on a router with no codec.
func (b *Base) Flashes() []Flash {
	cc := b.codec()
	if cc == nil {
		return nil
	}
	raw, ok := b.flashCookie(cc)
	if !ok || raw == "" {
		return nil
	}
	b.Vary(HeaderCookie)
	b.writeFlashCookie(b.NewCookie(FlashCookieName, "", -1))
	return decodeFlashes(cc, raw)
}

func (b *Base) flashes(cc *CookieCodec) []Flash {
	raw, ok := b.flashCookie(cc)
	if !ok {
		return nil
	}
	return decodeFlashes(cc, raw)
}

func decodeFlashes(cc *CookieCodec, raw string) []Flash {
	data, err := cc.Decode(FlashCookieName, raw)
	if err != nil {
		return nil
	}
	var flashes []Flash
	if err := json.Unmarshal(data, &flashes); err != nil {
		return nil
	}
	return flashes
}

// Reads the response before the request, so a second call sees what the first
// one wrote rather than handing the same messages out twice.
func (b *Base) flashCookie(cc *CookieCodec) (string, bool) {
	lines := b.res.Header()[headerSetCookie]
	for _, line := range slices.Backward(lines) {
		if c, err := http.ParseSetCookie(line); err == nil && c.Name == FlashCookieName {
			return c.Value, true
		}
	}
	cookies := b.req.CookiesNamed(FlashCookieName)
	if len(cookies) == 0 {
		return "", false
	}
	for _, c := range cookies {
		if _, err := cc.Decode(FlashCookieName, c.Value); err == nil {
			return c.Value, true
		}
	}
	return cookies[0].Value, true
}

func (b *Base) writeFlashCookie(c *http.Cookie) {
	line := c.String()
	if line == "" {
		return
	}
	header := b.res.Header()
	lines := header[headerSetCookie]
	for i, l := range lines {
		if got, err := http.ParseSetCookie(l); err == nil && got.Name == FlashCookieName {
			lines[i] = line
			return
		}
	}
	header[headerSetCookie] = append(lines, line)
}
