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
// Only a cookie the request carried is cleared on the client, so a message
// added and read in one request never leaves the server. Clearing is a
// header, so call Flashes before the response is committed.
// [Base.Render] buffers, so a template it runs may call Flashes; one that
// [Base.RenderStream] runs may not.
//
// It reports nothing on a router with no codec.
func (b *Base) Flashes() []Flash {
	cc := b.codec()
	if cc == nil {
		return nil
	}
	flashes, ok := b.flashCookie(cc)
	if !ok {
		return nil
	}
	b.Vary(HeaderCookie)
	if _, err := b.req.Cookie(FlashCookieName); err == nil {
		b.writeFlashCookie(b.NewCookie(FlashCookieName, "", -1))
	} else {
		b.dropFlashCookie()
	}
	return flashes
}

func (b *Base) flashes(cc *CookieCodec) []Flash {
	flashes, _ := b.flashCookie(cc)
	return flashes
}

// flashCookie reports the messages of the flash cookie, verified once. ok
// reports a cookie that holds a value, verified or not, which Flashes clears.
// It reads the response before the request, so a second call sees what the
// first one wrote rather than handing the same messages out twice.
func (b *Base) flashCookie(cc *CookieCodec) (flashes []Flash, ok bool) {
	lines := b.res.Header()[HeaderSetCookie]
	for _, line := range slices.Backward(lines) {
		if c, found := parseFlashLine(line); found {
			if c.Value == "" {
				return nil, false
			}
			data, err := cc.Decode(FlashCookieName, c.Value)
			if err != nil {
				return nil, true
			}
			return unmarshalFlashes(data), true
		}
	}
	cookies := b.req.CookiesNamed(FlashCookieName)
	if len(cookies) == 0 {
		return nil, false
	}
	for _, c := range cookies {
		if data, err := cc.Decode(FlashCookieName, c.Value); err == nil {
			return unmarshalFlashes(data), true
		}
	}
	return nil, cookies[0].Value != ""
}

func unmarshalFlashes(data []byte) []Flash {
	var flashes []Flash
	if err := json.Unmarshal(data, &flashes); err != nil {
		return nil
	}
	return flashes
}

func (b *Base) writeFlashCookie(c *http.Cookie) {
	line := c.String()
	if line == "" {
		return
	}
	header := b.res.Header()
	lines := header[HeaderSetCookie]
	for i, l := range lines {
		if _, ok := parseFlashLine(l); ok {
			lines[i] = line
			return
		}
	}
	header[HeaderSetCookie] = append(lines, line)
}

// dropFlashCookie takes back the flash cookie this response set, for a
// request that carried none and so has nothing to clear on the client.
func (b *Base) dropFlashCookie() {
	header := b.res.Header()
	lines := slices.DeleteFunc(header[HeaderSetCookie], func(l string) bool {
		_, ok := parseFlashLine(l)
		return ok
	})
	if len(lines) == 0 {
		delete(header, HeaderSetCookie)
		return
	}
	header[HeaderSetCookie] = lines
}

func parseFlashLine(line string) (*http.Cookie, bool) {
	c, err := http.ParseSetCookie(line)
	if err != nil || c.Name != FlashCookieName {
		return nil, false
	}
	return c, true
}
