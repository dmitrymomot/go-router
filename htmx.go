package router

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"
)

// The htmx headers of a request. [Base.HTMX] reads them all, and [Base.IsHTMX]
// reads HeaderHXRequest alone.
//
// The names carry the net/http canonical spelling, "Hx-Request" and not
// "HX-Request", so a lookup finds the key without building a new string.
const (
	// HeaderHXRequest carries "true" on every request that htmx makes.
	HeaderHXRequest = "Hx-Request"

	// HeaderHXBoosted carries "true" when the request comes from an element
	// under hx-boost, which asks for a whole page and not a fragment.
	HeaderHXBoosted = "Hx-Boosted"

	// HeaderHXCurrentURL carries the URL that the browser shows.
	HeaderHXCurrentURL = "Hx-Current-Url"

	// HeaderHXHistoryRestoreRequest carries "true" when htmx asks for a page
	// again because its history cache missed.
	HeaderHXHistoryRestoreRequest = "Hx-History-Restore-Request"

	// HeaderHXPrompt carries the answer that the user typed into an hx-prompt.
	HeaderHXPrompt = "Hx-Prompt"

	// HeaderHXTarget carries the id of the target element.
	HeaderHXTarget = "Hx-Target"

	// HeaderHXTriggerName carries the name of the element that triggered the
	// request.
	HeaderHXTriggerName = "Hx-Trigger-Name"

	// HeaderHXTrigger carries the id of the element that triggered the
	// request. On a response it names the events to fire.
	HeaderHXTrigger = "Hx-Trigger"
)

// The htmx headers of a response. [Base.HX] writes them.
const (
	// HeaderHXLocation asks for a client-side navigation without a full page
	// load.
	HeaderHXLocation = "Hx-Location"

	// HeaderHXPushURL pushes a URL onto the history stack.
	HeaderHXPushURL = "Hx-Push-Url"

	// HeaderHXRedirect asks the browser to go to another page.
	HeaderHXRedirect = "Hx-Redirect"

	// HeaderHXRefresh asks the browser to load the current page again.
	HeaderHXRefresh = "Hx-Refresh"

	// HeaderHXReplaceURL replaces the URL in the address bar.
	HeaderHXReplaceURL = "Hx-Replace-Url"

	// HeaderHXReswap names the swap style that htmx applies to this response.
	HeaderHXReswap = "Hx-Reswap"

	// HeaderHXRetarget names the element that this response swaps into.
	HeaderHXRetarget = "Hx-Retarget"

	// HeaderHXReselect names the part of this response that htmx swaps in.
	HeaderHXReselect = "Hx-Reselect"

	// HeaderHXTriggerAfterSettle names the events to fire after the settle
	// step.
	HeaderHXTriggerAfterSettle = "Hx-Trigger-After-Settle"

	// HeaderHXTriggerAfterSwap names the events to fire after the swap step.
	HeaderHXTriggerAfterSwap = "Hx-Trigger-After-Swap"
)

// The swap styles that [HXResponse.Reswap] takes. A style with a modifier,
// such as "innerHTML settle:100ms", has no constant; write it out.
const (
	HXSwapInnerHTML   = "innerHTML"
	HXSwapOuterHTML   = "outerHTML"
	HXSwapTextContent = "textContent"
	HXSwapBeforeBegin = "beforebegin"
	HXSwapAfterBegin  = "afterbegin"
	HXSwapBeforeEnd   = "beforeend"
	HXSwapAfterEnd    = "afterend"
	HXSwapDelete      = "delete"
	HXSwapNone        = "none"
)

// HTMXRequest holds the htmx headers that the client sent. [Base.HTMX] returns
// one.
//
// betteralign:check
type HTMXRequest struct {
	// CurrentURL is the URL the browser shows, not the URL of this request.
	CurrentURL string

	// Prompt is the answer that the user typed into an hx-prompt.
	Prompt string

	// Target is the id of the element the response swaps into.
	Target string

	// Trigger is the id of the element that triggered the request.
	Trigger string

	// TriggerName is the name attribute of the element that triggered the
	// request.
	TriggerName string

	// Request reports that htmx made this request. [Base.IsHTMX] answers it
	// without reading the other headers.
	Request bool

	// Boosted reports that the request comes from an element under hx-boost.
	// htmx swaps the whole body of the answer, so it wants a whole page.
	Boosted bool

	// HistoryRestore reports a history cache miss. Answer it with the whole
	// page.
	HistoryRestore bool
}

// HTMX returns the htmx headers the client sent. Every field is empty or false
// for a request htmx did not make. Reach for [Base.IsHTMX] to ask the one
// question most handlers ask, and for [Base.HX] to write the answer headers.
func (b *Base) HTMX() HTMXRequest {
	h := b.req.Header
	return HTMXRequest{
		CurrentURL:     h.Get(HeaderHXCurrentURL),
		Prompt:         h.Get(HeaderHXPrompt),
		Target:         h.Get(HeaderHXTarget),
		Trigger:        h.Get(HeaderHXTrigger),
		TriggerName:    h.Get(HeaderHXTriggerName),
		Request:        hxTrue(h.Get(HeaderHXRequest)),
		Boosted:        hxTrue(h.Get(HeaderHXBoosted)),
		HistoryRestore: hxTrue(h.Get(HeaderHXHistoryRestoreRequest)),
	}
}

// IsHTMX reports whether htmx made this request, from the HX-Request header.
//
// A boosted request reports true and still wants a whole page, so a handler
// that answers a fragment rules it out with [Base.IsBoosted] or reaches for
// [HTMXPartial]. A route that answers an htmx request differently has to tell
// a shared cache so, with c.Vary([router.HeaderHXRequest]).
func (b *Base) IsHTMX() bool { return hxTrue(b.req.Header.Get(HeaderHXRequest)) }

// IsBoosted reports whether the request comes from an element under hx-boost,
// from the HX-Boosted header. htmx swaps the whole body of that answer, so a
// boosted request wants the whole page. It is an htmx request as well.
func (b *Base) IsBoosted() bool { return hxTrue(b.req.Header.Get(HeaderHXBoosted)) }

// HTMXWantsPartial reports whether r takes an HTML fragment: htmx made it, and
// it is neither boosted nor a history restore, which both ask for a whole
// page. [HTMXPartial] and middleware.HTMXRedirect share this rule, so the two
// never disagree. It takes an [http.Request], which is what middleware outside
// this package holds.
func HTMXWantsPartial(r *http.Request) bool {
	h := r.Header
	return hxTrue(h.Get(HeaderHXRequest)) &&
		!hxTrue(h.Get(HeaderHXBoosted)) &&
		!hxTrue(h.Get(HeaderHXHistoryRestoreRequest))
}

// hxTrue reports whether a header carries the "true" that htmx sends.
func hxTrue(v string) bool { return strings.EqualFold(v, "true") }

// HTMXPartial returns a handler that answers an htmx request with partial and
// any other request with page, so one route serves both a fragment and the
// document around it. A boosted or history-restore request gets the page.
//
// It adds HX-Request and HX-Boosted to the Vary header: the same URL now has
// two answers, and a shared cache has to keep them apart. It panics when
// either handler is nil.
func HTMXPartial[C Context](partial, page HandlerFunc[C]) HandlerFunc[C] {
	if partial == nil || page == nil {
		panic("router: HTMXPartial needs both handlers")
	}
	return func(c C) error {
		b := c.base()
		b.Vary(HeaderHXRequest, HeaderHXBoosted)
		if HTMXWantsPartial(b.req) {
			return partial(c)
		}
		return page(c)
	}
}

// HXEvent is one client-side event that a response asks htmx to fire.
// [HXResponse.TriggerEvents] carries the detail to the listener as
// event.detail.
type HXEvent struct {
	// Detail is the payload of the event; nil sends null.
	Detail any

	// Name is the name that addEventListener takes.
	Name string
}

// HXLocation is the context of a client-side navigation.
// [HXResponse.LocationWith] writes it, and htmx then issues an ordinary
// request for Path and swaps its answer. Only Path is required, and an empty
// field stays out of the header.
type HXLocation struct {
	// Path is the URL to navigate to.
	Path string `json:"path"`

	// Target is the CSS selector of the element that the answer swaps into.
	Target string `json:"target,omitzero"`

	// Swap is the swap style, one of the HXSwap constants.
	Swap string `json:"swap,omitzero"`

	// Select is the CSS selector of the part of the answer to swap in.
	Select string `json:"select,omitzero"`

	// Source is the element that issues the request.
	Source string `json:"source,omitzero"`

	// Event is the event that triggers the request.
	Event string `json:"event,omitzero"`

	// Handler is the callback that handles the answer.
	Handler string `json:"handler,omitzero"`

	// Headers are extra request headers for the navigation.
	Headers map[string]string `json:"headers,omitzero"`

	// Values are the parameters that the navigation submits.
	Values map[string]string `json:"values,omitzero"`
}

// isPathOnly reports whether the location names a path alone, which is the
// short form of the header that htmx reads as a URL and not as JSON.
func (l HXLocation) isPathOnly() bool {
	return l.Target == "" && l.Swap == "" && l.Select == "" && l.Source == "" &&
		l.Event == "" && l.Handler == "" && len(l.Headers) == 0 && len(l.Values) == 0
}

// HXResponse writes the htmx headers of one response. [Base.HX] returns one.
// Each header method returns the value again, so a handler chains them and
// ends with a method that writes the body.
//
// A header the writer rejects, and a detail that fails to encode, stop the
// chain and reach the handler as the error of the method that ends it:
//
//	Render  RenderStream  HTML  String  JSON  NoContent
//	NoSwap  Redirect  Location  LocationWith
//
// The failure belongs to the request and not to the value, so a handler that
// drops one link still reports it. A chain that ends on none of the methods
// above has to read the failure with [HXResponse.Err], or nothing does.
type HXResponse struct {
	b *Base
}

// HX starts an htmx response. See [HXResponse] for the chain that it opens.
func (b *Base) HX() HXResponse { return HXResponse{b: b} }

// Err returns the first failure of the chain, nil when every header was
// written. Read it only when the chain ends on no body method, which reports
// the same error.
func (h HXResponse) Err() error { return h.b.hxError() }

// fail records the first failure of the chain.
func (h HXResponse) fail(err error) HXResponse {
	h.b.setHXError(ErrInternalServerError.WithError(err))
	return h
}

// set writes one header, unless the chain already failed.
//
// It rejects a value with a line break. net/http turns one into a space rather
// than a second header, so nothing is forgeable either way, but a selector or
// a URL that holds one is a bug worth reporting.
func (h HXResponse) set(name, value string) HXResponse {
	if h.b.hxError() != nil {
		return h
	}
	if strings.ContainsAny(value, "\r\n") {
		return h.fail(fmt.Errorf("router: the %s header holds a line break: %q", name, value))
	}
	h.b.res.Header().Set(name, value)
	return h
}

// PushURL pushes url onto the browser history stack, so that the address bar
// names the state that this response produced.
//
// Pass "false" to stop htmx from adding an entry that the element asked for.
func (h HXResponse) PushURL(url string) HXResponse {
	return h.set(HeaderHXPushURL, url)
}

// ReplaceURL replaces the current entry of the browser history, which leaves
// the back button where it was.
//
// Pass "false" to stop htmx from replacing an entry that the element asked
// for.
func (h HXResponse) ReplaceURL(url string) HXResponse {
	return h.set(HeaderHXReplaceURL, url)
}

// Retarget swaps this response into the element the CSS selector names, rather
// than the one the request asked for.
func (h HXResponse) Retarget(selector string) HXResponse {
	return h.set(HeaderHXRetarget, selector)
}

// Reselect swaps only the part of this response that the CSS selector names.
func (h HXResponse) Reselect(selector string) HXResponse {
	return h.set(HeaderHXReselect, selector)
}

// Reswap sets the swap style of this response, one of the HXSwap constants or
// a style with a modifier after it, such as "innerHTML settle:100ms".
func (h HXResponse) Reswap(swap string) HXResponse {
	return h.set(HeaderHXReswap, swap)
}

// Refresh asks the browser to load the current page again, and htmx drops this
// response without swapping it. It means nothing to a client that is not
// htmx, so a route that also answers a plain browser redirects that itself.
func (h HXResponse) Refresh() HXResponse {
	return h.set(HeaderHXRefresh, "true")
}

// Trigger fires the named events on the body of the page as soon as the
// response arrives. A listener reads them with addEventListener, and an
// element with hx-trigger answers them.
//
// Use [HXResponse.TriggerEvents] for an event with a payload, and for a name
// outside ASCII: the browser reads a header as one byte per character, so such
// a name arrives mangled unless the JSON form escapes it. A call without a
// name writes nothing.
func (h HXResponse) Trigger(names ...string) HXResponse {
	return h.triggerNames(HeaderHXTrigger, names)
}

// TriggerAfterSwap fires the named events once htmx swapped this response into
// the page.
func (h HXResponse) TriggerAfterSwap(names ...string) HXResponse {
	return h.triggerNames(HeaderHXTriggerAfterSwap, names)
}

// TriggerAfterSettle fires the named events once htmx finished the settle step
// that follows the swap.
func (h HXResponse) TriggerAfterSettle(names ...string) HXResponse {
	return h.triggerNames(HeaderHXTriggerAfterSettle, names)
}

// TriggerEvents fires events that carry a payload, which reaches a listener as
// event.detail. The header carries them as JSON, in the order given, encoded
// with the JSON options of the router and escaped outside ASCII. A call
// without an event writes nothing.
func (h HXResponse) TriggerEvents(events ...HXEvent) HXResponse {
	return h.triggerEvents(HeaderHXTrigger, events)
}

// TriggerEventsAfterSwap is [HXResponse.TriggerEvents] for the events that
// fire once htmx swapped this response into the page.
func (h HXResponse) TriggerEventsAfterSwap(events ...HXEvent) HXResponse {
	return h.triggerEvents(HeaderHXTriggerAfterSwap, events)
}

// TriggerEventsAfterSettle is [HXResponse.TriggerEvents] for the events that
// fire once htmx finished the settle step.
func (h HXResponse) TriggerEventsAfterSettle(events ...HXEvent) HXResponse {
	return h.triggerEvents(HeaderHXTriggerAfterSettle, events)
}

// triggerNames writes a comma separated list of event names.
func (h HXResponse) triggerNames(header string, names []string) HXResponse {
	if h.b.hxError() != nil || len(names) == 0 {
		return h
	}
	for i, n := range names {
		if err := validEventName(n); err != nil {
			return h.fail(fmt.Errorf(
				"router: the %s header cannot carry the event name %q: %w", header, n, err))
		}
		if slices.Contains(names[:i], n) {
			return h.fail(fmt.Errorf(
				"router: the %s header names the event %q twice", header, n))
		}
	}
	return h.set(header, strings.Join(names, ", "))
}

// validEventName reports why a name cannot go into the plain form of a trigger
// header, which is a comma separated list of ASCII names.
func validEventName(name string) error {
	switch {
	case name == "":
		return errEmptyEventName
	case strings.ContainsAny(name, ",\r\n"):
		return errEventNameSeparator
	case !isASCII(name):
		return errEventNameNotASCII
	default:
		return nil
	}
}

// The reasons [validEventName] reports, as values so a test compares them.
var (
	errEmptyEventName     = errors.New("an event name cannot be empty")
	errEventNameSeparator = errors.New("an event name cannot hold a comma or a line break")
	errEventNameNotASCII  = errors.New("an event name outside ASCII needs TriggerEvents, which escapes it")
)

// isASCII reports whether s holds only bytes below 0x80.
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// triggerEvents writes the JSON form of a trigger header.
func (h HXResponse) triggerEvents(header string, events []HXEvent) HXResponse {
	if h.b.hxError() != nil || len(events) == 0 {
		return h
	}

	// A repeated name writes the same object member twice: encoding/json/v2
	// refuses to read it, and the browser keeps the last one. The builder
	// writes the members itself, so the check lives here.
	var seen map[string]bool
	if len(events) > 1 {
		seen = make(map[string]bool, len(events))
	}

	var sb strings.Builder
	sb.WriteByte('{')
	for i, e := range events {
		if e.Name == "" {
			return h.fail(fmt.Errorf("router: the %s header holds an event without a name", header))
		}
		if seen != nil {
			if seen[e.Name] {
				return h.fail(fmt.Errorf(
					"router: the %s header names the event %q twice", header, e.Name))
			}
			seen[e.Name] = true
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		name, err := json.Marshal(e.Name, h.b.headerJSONOptions()...)
		if err != nil {
			return h.fail(fmt.Errorf(
				"router: encode the name of the %s event %q: %w", header, e.Name, err))
		}
		sb.Write(name)
		sb.WriteByte(':')

		detail, err := json.Marshal(e.Detail, h.b.headerJSONOptions()...)
		if err != nil {
			return h.fail(fmt.Errorf(
				"router: encode the detail of the %s event %q: %w", header, e.Name, err))
		}
		sb.Write(detail)
	}
	sb.WriteByte('}')
	return h.set(header, escapeNonASCII(sb.String()))
}

// escapeNonASCII rewrites every non-ASCII rune of a JSON text as a \u escape.
//
// A browser reads a response header as one byte per character, so UTF-8
// reaches the JSON parser of htmx mangled. Only a JSON string holds a byte
// above ASCII, and an escape there means the same thing as the character.
func escapeNonASCII(s string) string {
	if isASCII(s) {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for _, r := range s {
		if r < utf8.RuneSelf {
			sb.WriteByte(byte(r))
			continue
		}
		// A rune outside the basic multilingual plane takes a surrogate pair.
		if r > 0xFFFF {
			r -= 0x10000
			writeUnicodeEscape(&sb, 0xD800+(r>>10))
			writeUnicodeEscape(&sb, 0xDC00+(r&0x3FF))
			continue
		}
		writeUnicodeEscape(&sb, r)
	}
	return sb.String()
}

// writeUnicodeEscape writes one \uXXXX escape.
func writeUnicodeEscape(sb *strings.Builder, r rune) {
	const hex = "0123456789abcdef"
	sb.WriteString(`\u`)
	sb.WriteByte(hex[(r>>12)&0xF])
	sb.WriteByte(hex[(r>>8)&0xF])
	sb.WriteByte(hex[(r>>4)&0xF])
	sb.WriteByte(hex[r&0xF])
}

// Render writes an HTML body from a [Component], as [Base.Render] does.
func (h HXResponse) Render(status int, c Component) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.Render(status, c)
}

// RenderStream writes an HTML body straight to the client, as
// [Base.RenderStream] does.
func (h HXResponse) RenderStream(status int, c Component) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.RenderStream(status, c)
}

// HTML writes an HTML body, as [Base.HTML] does.
func (h HXResponse) HTML(status int, html string) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.HTML(status, html)
}

// String writes a plain text body, as [Base.String] does.
func (h HXResponse) String(status int, s string) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.String(status, s)
}

// JSON writes v as JSON, as [Base.JSON] does. htmx swaps that body like any
// other, so reach for it only when a listener of a triggered event reads it.
func (h HXResponse) JSON(status int, v any, opts ...json.Options) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.JSON(status, v, opts...)
}

// NoContent writes the status line alone, as [Base.NoContent] does.
func (h HXResponse) NoContent(status int) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.NoContent(status)
}

// NoSwap answers 204 No Content, which tells htmx to leave the page alone. The
// headers of the chain still apply, so it answers a request whose result
// reaches the page another way, such as a server-sent event.
func (h HXResponse) NoSwap() error { return h.NoContent(http.StatusNoContent) }

// Redirect sends the client to url.
//
// htmx follows a 3xx inside the request it made and swaps the answer into the
// element that asked, which is almost never what a redirect after a form post
// means. So this method sets HX-Redirect and answers 200 with an empty body.
// A request htmx did not make gets a plain 303 See Other.
func (h HXResponse) Redirect(url string) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	if !h.b.IsHTMX() {
		return h.b.Redirect(http.StatusSeeOther, url)
	}
	h = h.set(HeaderHXRedirect, url)
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.NoContent(http.StatusOK)
}

// Location navigates to path without a full page load: htmx issues an ordinary
// request for it and swaps the answer, which keeps the page and its scripts
// alive. Use [HXResponse.LocationWith] to choose the target and the swap. A
// request htmx did not make gets a plain 303 See Other.
func (h HXResponse) Location(path string) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	if !h.b.IsHTMX() {
		return h.b.Redirect(http.StatusSeeOther, path)
	}
	h = h.set(HeaderHXLocation, path)
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.NoContent(http.StatusOK)
}

// LocationWith is [HXResponse.Location] with the target, the swap and the rest
// of the navigation context. It reports an error when loc names no path.
func (h HXResponse) LocationWith(loc HXLocation) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	if loc.Path == "" {
		return h.fail(errors.New("router: an HX-Location needs a path")).b.hxError()
	}
	if !h.b.IsHTMX() {
		return h.b.Redirect(http.StatusSeeOther, loc.Path)
	}

	if loc.isPathOnly() {
		return h.Location(loc.Path)
	}

	data, err := json.Marshal(loc, h.b.headerJSONOptions()...)
	if err != nil {
		return h.fail(fmt.Errorf("router: encode the HX-Location header: %w", err)).b.hxError()
	}
	h = h.set(HeaderHXLocation, escapeNonASCII(string(data)))
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.NoContent(http.StatusOK)
}

// headerJSONOptions are the options a header value encodes with: those of the
// router, then Multiline off, because a header takes no indented value.
func (b *Base) headerJSONOptions() []json.Options {
	return b.jsonOptions([]json.Options{jsontext.Multiline(false)})
}
