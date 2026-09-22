package cookie

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/dmitrymomot/go-router"
)

// FlashName is the cookie that carries the flash messages.
const FlashName = "_flash"

// FlashMaxAge is how long a flash message survives before the browser drops
// it.
const FlashMaxAge = 5 * time.Minute

// Flash is one message that survives a redirect. Kind is yours to choose,
// such as "error" or "success".
type Flash struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// AddFlash appends f to the flash cookie, which cc signs. The cookie is the
// one [router.Base.NewCookie] builds: HttpOnly, SameSite=Lax, and Secure over
// HTTPS.
//
// The message travels with whatever this response is: a redirect, an
// HX-Redirect or a page. [Codec.Flashes] in the same request reads it back, so
// an htmx partial can show it in place.
//
// It reports [ErrTooLarge] when the messages no longer fit in one cookie. The
// cookie is then left as it was, so no message is lost, and the caller has to
// shorten or drop one.
func (cc *Codec) AddFlash(c router.Context, f Flash) error {
	cc.mustBeBuilt()
	flashes, _ := cc.flashCookie(c)
	flashes = append(flashes, f)

	data, err := json.Marshal(flashes)
	if err != nil {
		return fmt.Errorf("cookie: encode the flash messages: %w", err)
	}

	router.AddVary(c.Response().Header(), router.HeaderCookie)

	ck := newCookie(c, FlashName, "", FlashMaxAge)
	ck.Value = cc.Encode(ck.Name, data, expiryOf(ck, time.Now()))
	if len(ck.String()) > MaxSize {
		return ErrTooLarge
	}
	writeFlashCookie(c, ck)
	return nil
}

// Flashes reports the messages the request of c carried, then those this
// response added, and clears them, so each is shown once. A second call in the
// same request reports nothing. Add first, then read: a layout that calls
// Flashes sees what the handler added before it.
//
// Only a cookie the request carried is cleared on the client, so a message
// added and read in one request never leaves the server. Clearing is a
// header, so call Flashes before the response is committed.
// [router.Base.Render] buffers, so a template it runs may call Flashes; one
// that [router.Base.RenderStream] runs may not.
func (cc *Codec) Flashes(c router.Context) []Flash {
	cc.mustBeBuilt()
	flashes, ok := cc.flashCookie(c)
	if !ok {
		return nil
	}
	router.AddVary(c.Response().Header(), router.HeaderCookie)
	if _, err := c.Request().Cookie(FlashName); err == nil {
		writeFlashCookie(c, newCookie(c, FlashName, "", -1))
	} else {
		dropFlashCookie(c)
	}
	return flashes
}

// flashCookie reports the messages of the flash cookie, verified once. ok
// reports a cookie that holds a value, verified or not, which Flashes clears.
// It reads the response before the request, so a second call sees what the
// first one wrote rather than handing the same messages out twice.
func (cc *Codec) flashCookie(c router.Context) (flashes []Flash, ok bool) {
	lines := c.Response().Header()[router.HeaderSetCookie]
	for _, line := range slices.Backward(lines) {
		if ck, found := parseFlashLine(line); found {
			if ck.Value == "" {
				return nil, false
			}
			data, err := cc.Decode(FlashName, ck.Value)
			if err != nil {
				return nil, true
			}
			return unmarshalFlashes(data), true
		}
	}
	cookies := c.Request().CookiesNamed(FlashName)
	if len(cookies) == 0 {
		return nil, false
	}
	for _, ck := range cookies {
		if data, err := cc.Decode(FlashName, ck.Value); err == nil {
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

// writeFlashCookie sets ck on the response of c, in place of a flash cookie
// the response already sets.
func writeFlashCookie(c router.Context, ck *http.Cookie) {
	line := ck.String()
	if line == "" {
		return
	}
	header := c.Response().Header()
	lines := header[router.HeaderSetCookie]
	for i, l := range lines {
		if _, ok := parseFlashLine(l); ok {
			lines[i] = line
			return
		}
	}
	header[router.HeaderSetCookie] = append(lines, line)
}

// dropFlashCookie takes back the flash cookie this response set, for a
// request that carried none and so has nothing to clear on the client.
func dropFlashCookie(c router.Context) {
	header := c.Response().Header()
	lines := slices.DeleteFunc(header[router.HeaderSetCookie], func(l string) bool {
		_, ok := parseFlashLine(l)
		return ok
	})
	if len(lines) == 0 {
		delete(header, router.HeaderSetCookie)
		return
	}
	header[router.HeaderSetCookie] = lines
}

func parseFlashLine(line string) (*http.Cookie, bool) {
	ck, err := http.ParseSetCookie(line)
	if err != nil || ck.Name != FlashName {
		return nil, false
	}
	return ck, true
}
