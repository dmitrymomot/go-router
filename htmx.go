package router

import (
	"log/slog"
	"strings"
)

// hxTrue is the value that htmx sends for a header that carries a flag.
const hxTrue = "true"

// hxFlag reports whether the named request header carries the htmx flag.
func (b *Base) hxFlag(name string) bool {
	return strings.EqualFold(b.req.Header.Get(name), hxTrue)
}

// HXRequest reports whether htmx sent the request. It reads the HX-Request
// header, which htmx sets on every request it makes:
//
//	if c.HXRequest() {
//		return c.Render(http.StatusOK, view.Rows(rows))
//	}
//	return c.Render(http.StatusOK, view.Page(rows))
//
// [Base.RenderPartial] is that branch in one call, and it sets the Vary header
// that a cache needs.
func (b *Base) HXRequest() bool { return b.hxFlag(HeaderHXRequest) }

// HXBoosted reports whether the request comes from an element under hx-boost,
// which is a link or a form that htmx took over. Such a request replaces the
// body of the page, so it takes the whole page as its answer and not a
// fragment.
func (b *Base) HXBoosted() bool { return b.hxFlag(HeaderHXBoosted) }

// HXHistoryRestore reports whether htmx asks for the page again to restore a
// history entry, because its cache no longer holds it. Answer one with the
// whole page.
func (b *Base) HXHistoryRestore() bool { return b.hxFlag(HeaderHXHistoryRestore) }

// HXTarget returns the id of the element that receives the answer, without the
// "#". It is empty when the target carries no id.
func (b *Base) HXTarget() string { return b.req.Header.Get(HeaderHXTarget) }

// HXTrigger returns the id of the element that fired the request. It is empty
// when that element carries no id.
func (b *Base) HXTrigger() string { return b.req.Header.Get(HeaderHXTrigger) }

// HXTriggerName returns the name of the element that fired the request, which
// is the name attribute of a form field.
func (b *Base) HXTriggerName() string { return b.req.Header.Get(HeaderHXTriggerName) }

// HXPrompt returns what the user typed into the dialog that hx-prompt raised.
//
// It is text that the client sent, so it needs the same validation as any
// other field of a request.
func (b *Base) HXPrompt() string { return b.req.Header.Get(HeaderHXPrompt) }

// HXCurrentURL returns the URL of the browser at the time of the request. Read
// it to answer a fragment differently depending on the page that asked.
//
// The client sends it, so a handler that acts on it validates it first.
func (b *Base) HXCurrentURL() string { return b.req.Header.Get(HeaderHXCurrentURL) }

// setHX sets an htmx response header.
//
// A value that holds a carriage return or a line feed sets no header at all and
// logs the attempt at error level, because such a value ends the header early
// and writes the rest of the response itself. The log names the header and not
// the value, which keeps the same bytes out of the log record.
func (b *Base) setHX(name, value string) {
	if strings.ContainsAny(value, "\r\n") {
		b.Logger().ErrorContext(b.req.Context(), "router: the htmx header value holds a line break",
			slog.String("header", name),
			slog.String("path", b.req.URL.Path),
			slog.String("route", b.pattern),
		)
		return
	}
	b.res.Header().Set(name, value)
}

// HXRedirect sets the HX-Redirect header, which makes htmx send the browser to
// url with a full page load. Use it to answer a form that finished with a page
// somewhere else.
//
// The redirect travels in a header of the answer rather than in a Location, so
// the status stays 200 and htmx alone reads it. A browser that runs no htmx
// stays where it is.
//
// A url that holds a line break sets no header at all, because such a value
// ends the header and writes a response of its own after it. That check is not
// validation: htmx follows this URL as readily as a browser follows a
// Location, so a destination that came out of the request is the caller's to
// check first.
func (b *Base) HXRedirect(url string) { b.setHX(HeaderHXRedirect, url) }

// HXLocation sets the HX-Location header, which makes htmx fetch url and swap
// the answer in without reloading the page. Pass a JSON object instead of a
// plain URL to name the target and the swap style.
//
// It carries the same risk as [Base.HXRedirect] and takes the same care: a
// value with a line break sets no header, and a destination that came out of
// the request is the caller's to check first.
func (b *Base) HXLocation(url string) { b.setHX(HeaderHXLocation, url) }

// HXPushURL sets the HX-Push-Url header, which puts url in the address bar and
// on the history stack. Pass "false" to keep htmx from pushing anything.
func (b *Base) HXPushURL(url string) { b.setHX(HeaderHXPushURL, url) }

// HXReplaceURL sets the HX-Replace-Url header, which puts url in the address
// bar in place of the current entry, so the back button skips it. Pass "false"
// to keep htmx from replacing anything.
func (b *Base) HXReplaceURL(url string) { b.setHX(HeaderHXReplaceURL, url) }

// HXRetarget sets the HX-Retarget header, which sends the answer to another
// element. sel is a CSS selector:
//
//	c.HXRetarget("#errors")
func (b *Base) HXRetarget(sel string) { b.setHX(HeaderHXRetarget, sel) }

// HXReswap sets the HX-Reswap header, which overrides the hx-swap of the
// element that fired the request. spec is a swap style, such as "outerHTML" or
// "beforeend".
func (b *Base) HXReswap(spec string) { b.setHX(HeaderHXReswap, spec) }

// HXTriggerEvent sets the HX-Trigger header, which fires an event on the
// client once the answer lands:
//
//	c.HXTriggerEvent("orderPlaced")
//
// Pass a JSON object as the name to carry a payload:
// `{"orderPlaced":{"id":7}}` fires one event with a detail.
func (b *Base) HXTriggerEvent(name string) { b.setHX(HeaderHXTrigger, name) }

// HXRefresh sets the HX-Refresh header, which makes the client reload the
// whole page. Use it when a change reaches more of the page than one swap
// covers.
func (b *Base) HXRefresh() { b.res.Header().Set(HeaderHXRefresh, hxTrue) }

// varyHTMX names every request header that [Base.RenderPartial] reads. A cache
// that keys on all three keeps the two answers of a route apart.
const varyHTMX = HeaderHXRequest + ", " + HeaderHXBoosted + ", " + HeaderHXHistoryRestore

// RenderPartial writes fragment for an htmx request and full for a normal one,
// so that one route answers both a navigation and a swap:
//
//	r.GET("/orders", func(c *app.Context) error {
//		orders, err := c.Store.Orders(c)
//		if err != nil {
//			return err
//		}
//		return c.RenderPartial(http.StatusOK, view.Orders(orders), view.OrderRows(orders))
//	})
//
// Two htmx requests read the whole page all the same, because both swap more
// of the document than a fragment covers: a boosted link or form, which
// replaces the body, and a request that restores a history entry. That leaves
// the fragment for the swap that asked for one.
//
// It adds a Vary header that names those three request headers, so that a
// shared cache keeps the answers apart. Without it the cache hands the
// fragment to the next browser that asks for the page.
//
// Both components go through [Base.Render], which buffers, so one that fails
// halfway answers 500 and leaves no partial page on the wire.
func (b *Base) RenderPartial(status int, full, fragment Component) error {
	b.res.Header().Add(HeaderVary, varyHTMX)
	if b.HXRequest() && !b.HXBoosted() && !b.HXHistoryRestore() {
		return b.Render(status, fragment)
	}
	return b.Render(status, full)
}
