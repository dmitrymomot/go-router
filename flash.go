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

// FlashMaxAge is the lifetime of that cookie. A flash crosses one redirect, so
// the window is the round trip and not the visit.
const FlashMaxAge = 5 * time.Minute

// headerSetCookie is the response header that carries a cookie. It is already
// in canonical form, which is what lets the flash helpers index the header map
// with it.
const headerSetCookie = "Set-Cookie"

// ErrFlashTooLarge means that the messages no longer fit in one cookie.
var ErrFlashTooLarge = errors.New("router: the flash messages do not fit in one cookie")

// Flash is a message that a handler leaves for the next request, the way a
// POST that succeeds leaves "saved" and one that does not leaves the errors of
// the form. Kind names the sort of message, and the template turns it into a
// class:
//
//	for _, f := range c.Flashes(codec) {
//		<div class={ "alert", "alert-" + f.Kind }>{ f.Message }</div>
//	}
//
// The messages live in a signed cookie and nowhere else. This is not a session
// package: it keeps no server-side store, mints no session identifier and
// collects nothing, and a message that the browser drops is gone. That buys a
// server that holds no state between two requests, and it costs room. A
// browser guarantees [MaxCookieSize] bytes for one cookie, attributes
// included, and caps how many cookies a domain holds, so a flash carries a
// sentence and a kind and never a form.
type Flash struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// AddFlash appends f to the flash cookie of the response. Call it as often as
// the handler has something to say, then redirect:
//
//	if err := c.AddFlash(codec, router.Flash{Kind: "error", Message: "that name is taken"}); err != nil {
//		return err
//	}
//	return c.Redirect(http.StatusSeeOther, "/signup")
//
// It reads the messages that the request carries and the ones that an earlier
// call in the same response wrote, so the cookie holds them all in the order
// that the calls added them. A cookie that does not verify, or one whose
// expiry passed, reads as no messages and takes no message down with it.
//
// It reports [ErrFlashTooLarge] when the cookie passes [MaxCookieSize], and
// leaves the cookie that the response already carries in place, because a
// cookie over the limit is one that the browser drops whole. Redirect anyway
// and the user sees the messages that fit.
//
// Write it before the handler writes the body. A header that a committed
// response gains never reaches the client.
func (b *Base) AddFlash(cc *CookieCodec, f Flash) error {
	flashes := b.flashes(cc)
	flashes = append(flashes, f)

	// The options of the router belong to the payloads of the application. The
	// cookie is a format of this package, so it encodes the same way whatever
	// the application configured.
	data, err := json.Marshal(flashes)
	if err != nil {
		return fmt.Errorf("router: encode the flash messages: %w", err)
	}

	// The answer carries a Set-Cookie built from the cookie that the request
	// sent, so a shared cache that keyed it on the URL alone would hand the next
	// user this list of messages.
	b.Vary(HeaderCookie)

	c := b.flashTemplate()
	c.MaxAge = int(FlashMaxAge / time.Second)
	c.Value = cc.encode(c.Name, data, signedExpiry(cc, c, time.Now()))
	if len(c.String()) > MaxCookieSize {
		return ErrFlashTooLarge
	}
	b.writeFlashCookie(c)
	return nil
}

// Flashes returns the messages that [Base.AddFlash] left and clears the
// cookie. It reads and clears in one call, which is the contract that makes a
// flash a flash: a message reaches one page and no more.
//
//	return c.Render(http.StatusOK, view.Signup(c.Flashes(codec)))
//
// It clears by writing an expired cookie, so the answer carries the clearing
// header even when the handler goes on to render a page. A second call answers
// with nothing, because it reads what the response already says. A cookie that
// does not verify, or one whose expiry passed, reads as no messages and clears
// the same way. A request that carries two cookies of the name reads the first
// that verifies, which is the one that this server wrote.
//
// The answer carries Vary: Cookie, so that a shared cache cannot hand one user
// the messages of another.
//
// Read it before the handler writes the body. A header that a committed
// response gains never reaches the client.
func (b *Base) Flashes(cc *CookieCodec) []Flash {
	raw, ok := b.flashCookie(cc)
	if !ok || raw == "" {
		return nil
	}
	b.Vary(HeaderCookie)
	b.clearFlashCookie()
	return decodeFlashes(cc, raw)
}

// flashes returns the messages that the cookie carries, without clearing it.
func (b *Base) flashes(cc *CookieCodec) []Flash {
	raw, ok := b.flashCookie(cc)
	if !ok {
		return nil
	}
	return decodeFlashes(cc, raw)
}

// decodeFlashes returns the messages inside a signed value, and nothing for a
// value that this codec did not sign or that no longer holds a list.
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

// flashTemplate returns the flash cookie without a value, which is the shape
// that both writing and clearing it take. The two match, because a browser
// replaces a cookie only when the name, the domain and the path all agree.
func (b *Base) flashTemplate() *http.Cookie {
	return &http.Cookie{
		Name:     FlashCookieName,
		Path:     "/",
		Secure:   b.Scheme() == "https",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// clearFlashCookie writes a flash cookie that expired, which is what tells the
// browser to drop the one it holds.
func (b *Base) clearFlashCookie() {
	c := b.flashTemplate()
	c.MaxAge = -1
	b.writeFlashCookie(c)
}

// flashCookie returns the raw value of the flash cookie, and reports whether
// it found one.
//
// It reads the response before the request, so that a second call sees what
// the first one wrote: the messages that an earlier AddFlash added, or the
// empty value that the clearing header left. A helper that read the request
// alone would add a message to a list that another call already replaced, and
// would hand the messages out twice.
//
// A request that carries two cookies of the name takes the first that
// verifies, the way [Base.SignedCookie] does. It falls back to the first of
// them when none verifies, because the caller clears what it found and a
// cookie that stays behind shadows every later flash.
func (b *Base) flashCookie(cc *CookieCodec) (string, bool) {
	lines := b.res.Header()[headerSetCookie]
	// The last header of a name is the one that the browser keeps.
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

// writeFlashCookie writes c in place of the flash cookie that an earlier call
// wrote, and appends it when the response carries none.
//
// A response that repeated the header would carry every intermediate list of
// messages, which costs room in an answer that a proxy caps and leaves the
// browser to settle which one wins.
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
