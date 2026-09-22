// Package htmx serves htmx 4 from go-router.
//
// [RequestOf] reads the htmx headers of a request. [WantsPartial],
// [RenderPartial] and [Partial] answer a request that swaps one element with a
// fragment, and any other with the whole page. [NewResponse] builds the htmx
// headers of an answer, as in
//
//	hx := htmx.NewResponse(c).Retarget("#user-7").Trigger("user-saved")
//	if err := hx.Err(); err != nil {
//		return err
//	}
//	return c.Render(http.StatusOK, card)
//
// These are the headers htmx 4 sends; htmx 2 is not supported.
package htmx

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dmitrymomot/go-router"
)

// The htmx headers that a request carries. [RequestOf] reads them into a
// [Request].
const (
	HeaderRequest               = "Hx-Request"
	HeaderRequestType           = "Hx-Request-Type"
	HeaderBoosted               = "Hx-Boosted"
	HeaderCurrentURL            = "Hx-Current-Url"
	HeaderHistoryRestoreRequest = "Hx-History-Restore-Request"
	HeaderSource                = "Hx-Source"
	HeaderTarget                = "Hx-Target"
)

// The htmx headers that a response carries. [Response] sets them.
const (
	HeaderLocation   = "Hx-Location"
	HeaderPushURL    = "Hx-Push-Url"
	HeaderRedirect   = "Hx-Redirect"
	HeaderRefresh    = "Hx-Refresh"
	HeaderReplaceURL = "Hx-Replace-Url"
	HeaderReswap     = "Hx-Reswap"
	HeaderRetarget   = "Hx-Retarget"
	HeaderReselect   = "Hx-Reselect"
	HeaderTrigger    = "Hx-Trigger"
)

// The swap styles that [Response.Reswap] takes.
const (
	SwapInnerHTML   = "innerHTML"
	SwapOuterHTML   = "outerHTML"
	SwapTextContent = "textContent"
	SwapBeforeBegin = "beforebegin"
	SwapAfterBegin  = "afterbegin"
	SwapBeforeEnd   = "beforeend"
	SwapAfterEnd    = "afterend"
	SwapDelete      = "delete"
	SwapNone        = "none"
)

// Request is what the htmx headers of one request say. Request is false when
// htmx did not make the request, and the other fields are then empty.
//
// RequestType is "full" when htmx swaps the whole body or selects part of the
// answer, as for a boosted link or a history restore, and "partial" when it
// swaps one element; it is what [WantsPartial] reads. Source names the element
// that made the request and Target the one the answer goes into, both in the
// tag#id form htmx 4 sends, such as "ul#user-list". [Request.TargetID] and
// [Request.SourceID] read the id alone.
//
// Request says who sent the request, not what to answer: a boosted link and a
// history restore come from htmx too and want a whole page. [WantsPartial]
// decides that.
//
// htmx 4 swaps a 4xx or 5xx answer into the target too, so a scope that serves
// htmx wants an error handler that renders a fragment from
// [router.HTTPErrorOf].
//
//betteralign:check
type Request struct {
	CurrentURL     string
	RequestType    string
	Source         string
	Target         string
	Request        bool
	Boosted        bool
	HistoryRestore bool
}

// RequestOf reads the htmx headers of r. It reports the zero Request when r
// carries no HX-Request: true.
func RequestOf(r *http.Request) Request {
	h := r.Header
	if !isTrue(h.Get(HeaderRequest)) {
		return Request{}
	}
	return Request{
		CurrentURL:     h.Get(HeaderCurrentURL),
		RequestType:    h.Get(HeaderRequestType),
		Source:         h.Get(HeaderSource),
		Target:         h.Get(HeaderTarget),
		Request:        true,
		Boosted:        isTrue(h.Get(HeaderBoosted)),
		HistoryRestore: isTrue(h.Get(HeaderHistoryRestoreRequest)),
	}
}

// TargetID reports the id of the element the answer goes into: "user-list"
// for the target "ul#user-list". It is "" when the target has no id.
func (h Request) TargetID() string { return idOf(h.Target) }

// SourceID reports the id of the element that made the request: "delete-7"
// for the source "button#delete-7". It is "" when the source has no id.
func (h Request) SourceID() string { return idOf(h.Source) }

// idOf reads the id out of the tag#id form of htmx 4, which escapes the id
// with encodeURI. An escape that does not decode is returned as sent.
func idOf(v string) string {
	_, id, ok := strings.Cut(v, "#")
	if !ok {
		return ""
	}
	if s, err := url.PathUnescape(id); err == nil {
		return s
	}
	return id
}

func isTrue(v string) bool { return strings.EqualFold(v, "true") }

// isHTMX reports whether htmx made r.
func isHTMX(r *http.Request) bool { return isTrue(r.Header.Get(HeaderRequest)) }

// WantsPartial reports whether the request of c wants a fragment rather than a
// whole page: htmx made it, and its HX-Request-Type is not "full". htmx 4
// sends "full" for a boosted link, a history restore, and a request that swaps
// the whole body or selects part of the answer. A request without the type
// counts as partial. htmx 2 sends no type, so it is not supported: its boosted
// links and history restores would get fragments.
//
// It adds HX-Request and HX-Request-Type to Vary, so a cache keeps the two
// answers apart.
func WantsPartial(c router.Context) bool {
	router.AddVary(c.Response().Header(), HeaderRequest, HeaderRequestType)
	r := c.Request()
	return isHTMX(r) && !strings.EqualFold(r.Header.Get(HeaderRequestType), "full")
}

// RenderPartial renders partial with status for a request that wants a
// fragment, and page for any other. See [WantsPartial] and
// [router.Base.Render].
func RenderPartial(c router.Context, status int, partial, page router.Component) error {
	b := baseOf(c)
	if WantsPartial(c) {
		return b.Render(status, partial)
	}
	return b.Render(status, page)
}

// Partial picks between two handlers for one route: partial for a request
// that wants a fragment, page for anything else. See [WantsPartial].
//
// Partial panics if either handler is nil.
func Partial[C router.Context](partial, page router.HandlerFunc[C]) router.HandlerFunc[C] {
	if partial == nil || page == nil {
		panic("htmx: Partial needs both handlers")
	}
	return func(c C) error {
		if WantsPartial(c) {
			return partial(c)
		}
		return page(c)
	}
}

// baseOf reports the Base of c, or a Base over its request and response when
// c hides it.
func baseOf(c router.Context) *router.Base {
	if b, ok := router.FromContext(c); ok {
		return b
	}
	return router.NewBase(c.Response(), c.Request())
}

// Event is one client-side event, with a detail that goes out as JSON. See
// [Response.TriggerEvents].
type Event struct {
	Detail any
	Name   string
}

// Location is a client-side navigation with the details of the swap. Path is
// the only required field. See [Response.LocationWith].
type Location struct {
	Path    string            `json:"path"`
	Target  string            `json:"target,omitzero"`
	Swap    string            `json:"swap,omitzero"`
	Select  string            `json:"select,omitzero"`
	Source  string            `json:"source,omitzero"`
	Event   string            `json:"event,omitzero"`
	Headers map[string]string `json:"headers,omitzero"`
	Values  map[string]string `json:"values,omitzero"`
}

func (l Location) isPathOnly() bool {
	return l.Target == "" && l.Swap == "" && l.Select == "" && l.Source == "" &&
		l.Event == "" && len(l.Headers) == 0 && len(l.Values) == 0
}

// Response builds the htmx headers of one answer. The header methods chain.
//
// A method that is handed something a header cannot carry records the failure
// and writes nothing. Every later method of the same Response then does
// nothing, so a chain needs one check of [Response.Err] at its end and not one
// per call. The failure stays in the Response: another [NewResponse] in the
// same request starts clean.
//
// The navigation methods, [Response.Redirect], [Response.Location],
// [Response.LocationWith] and [Response.NoSwap], end the chain and report the
// failure. To answer with a body, check Err and then call the method of the
// context, such as [router.Base.Render].
type Response struct {
	b   *router.Base
	err error
}

// NewResponse opens the htmx headers of the answer to c. See [Response].
func NewResponse(c router.Context) *Response { return &Response{b: baseOf(c)} }

// Err reports the first failure of the chain, or nil.
func (r *Response) Err() error { return r.err }

func (r *Response) fail(err error) *Response {
	if r.err == nil {
		r.err = router.ErrInternalServerError.WithError(err)
	}
	return r
}

func (r *Response) set(name, value string) *Response {
	if r.err != nil {
		return r
	}
	if err := validHeaderValue(value); err != nil {
		return r.fail(fmt.Errorf("htmx: the %s header cannot carry %q: %w", name, value, err))
	}
	r.b.Response().Header().Set(name, value)
	return r
}

var errHeaderByte = errors.New("a header value holds only printable ASCII")

// validHeaderValue refuses a control byte, which could split the header, and
// a byte outside ASCII, which a browser reads one byte per character.
func validHeaderValue(v string) error {
	for i := range len(v) {
		if c := v[i]; c < 0x20 || c == 0x7f || c >= utf8.RuneSelf {
			return errHeaderByte
		}
	}
	return nil
}

// PushURL puts url in the history of the browser.
func (r *Response) PushURL(url string) *Response {
	return r.set(HeaderPushURL, url)
}

// ReplaceURL replaces the current entry in the history of the browser with
// url.
func (r *Response) ReplaceURL(url string) *Response {
	return r.set(HeaderReplaceURL, url)
}

// Retarget swaps the answer into the element that selector names, in place of
// the one the request targeted.
func (r *Response) Retarget(selector string) *Response {
	return r.set(HeaderRetarget, selector)
}

// Reselect takes the part of the answer that selector names, in place of the
// whole body.
func (r *Response) Reselect(selector string) *Response {
	return r.set(HeaderReselect, selector)
}

// Reswap changes how the answer is swapped in. See the Swap constants.
func (r *Response) Reswap(swap string) *Response {
	return r.set(HeaderReswap, swap)
}

// Refresh tells the client to reload the whole page.
func (r *Response) Refresh() *Response {
	return r.set(HeaderRefresh, "true")
}

// Trigger fires the named events on the element that made the request, once
// htmx has swapped the answer in. The events bubble. A name has to be ASCII,
// and it cannot be empty, hold a comma or a line break, or repeat; use
// [Response.TriggerEvents] for anything else.
func (r *Response) Trigger(names ...string) *Response {
	if r.err != nil || len(names) == 0 {
		return r
	}
	for i, n := range names {
		if err := validEventName(n); err != nil {
			return r.fail(fmt.Errorf(
				"htmx: the %s header cannot carry the event name %q: %w", HeaderTrigger, n, err))
		}
		if slices.Contains(names[:i], n) {
			return r.fail(fmt.Errorf(
				"htmx: the %s header names the event %q twice", HeaderTrigger, n))
		}
	}
	return r.set(HeaderTrigger, strings.Join(names, ", "))
}

// TriggerEvents is [Response.Trigger] with a detail for each event, sent as
// JSON. A name outside ASCII is escaped, so any name that is not empty and
// does not repeat works here.
func (r *Response) TriggerEvents(events ...Event) *Response {
	if r.err != nil || len(events) == 0 {
		return r
	}

	var seen map[string]bool
	if len(events) > 1 {
		seen = make(map[string]bool, len(events))
	}

	var sb strings.Builder
	sb.WriteByte('{')
	for i, e := range events {
		if e.Name == "" {
			return r.fail(fmt.Errorf("htmx: the %s header holds an event without a name", HeaderTrigger))
		}
		if seen != nil {
			if seen[e.Name] {
				return r.fail(fmt.Errorf(
					"htmx: the %s header names the event %q twice", HeaderTrigger, e.Name))
			}
			seen[e.Name] = true
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		name, err := json.Marshal(e.Name)
		if err != nil {
			return r.fail(fmt.Errorf(
				"htmx: encode the name of the %s event %q: %w", HeaderTrigger, e.Name, err))
		}
		sb.Write(name)
		sb.WriteByte(':')

		detail, err := json.Marshal(e.Detail)
		if err != nil {
			return r.fail(fmt.Errorf(
				"htmx: encode the detail of the %s event %q: %w", HeaderTrigger, e.Name, err))
		}
		sb.Write(detail)
	}
	sb.WriteByte('}')
	return r.set(HeaderTrigger, escapeNonASCII(sb.String()))
}

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

var (
	errEmptyEventName     = errors.New("an event name cannot be empty")
	errEventNameSeparator = errors.New("an event name cannot hold a comma or a line break")
	errEventNameNotASCII  = errors.New("an event name outside ASCII needs TriggerEvents, which escapes it")
)

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// A browser reads a header as one byte per character, so UTF-8 reaches the
// JSON parser of htmx mangled.
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

func writeUnicodeEscape(sb *strings.Builder, r rune) {
	const hex = "0123456789abcdef"
	sb.WriteString(`\u`)
	sb.WriteByte(hex[(r>>12)&0xF])
	sb.WriteByte(hex[(r>>8)&0xF])
	sb.WriteByte(hex[(r>>4)&0xF])
	sb.WriteByte(hex[r&0xF])
}

// NoSwap answers 204, which tells htmx to swap nothing, or reports the failure
// of the chain. The headers of the chain still reach the client.
func (r *Response) NoSwap() error {
	if r.err != nil {
		return r.err
	}
	return r.b.NoContent(http.StatusNoContent)
}

// navigate sets one of the htmx navigation headers and answers 200, or falls
// back to an ordinary 303 for a request htmx did not make.
func (r *Response) navigate(header, target string) error {
	if r.err != nil {
		return r.err
	}
	if target == "" {
		return r.fail(fmt.Errorf("htmx: the %s header needs a URL", header)).err
	}
	if !isHTMX(r.b.Request()) {
		return r.b.Redirect(http.StatusSeeOther, target)
	}
	if r.set(header, target).err != nil {
		return r.err
	}
	return r.b.NoContent(http.StatusOK)
}

// Redirect sends the browser to url as a whole-page load. A request that htmx
// did not make gets an ordinary 303 instead. It reports a failure when url is
// empty.
func (r *Response) Redirect(url string) error {
	return r.navigate(HeaderRedirect, url)
}

// Location sends the client to path as a swap, which keeps the page loaded. A
// request that htmx did not make gets an ordinary 303 instead. It reports a
// failure when path is empty.
func (r *Response) Location(path string) error {
	return r.navigate(HeaderLocation, path)
}

// LocationWith is [Response.Location] with the details of the swap. It reports
// a failure when loc names no path.
func (r *Response) LocationWith(loc Location) error {
	if r.err != nil {
		return r.err
	}
	if loc.Path == "" || !isHTMX(r.b.Request()) || loc.isPathOnly() {
		return r.Location(loc.Path)
	}

	data, err := json.Marshal(loc)
	if err != nil {
		return r.fail(fmt.Errorf("htmx: encode the %s header: %w", HeaderLocation, err)).err
	}
	if r.set(HeaderLocation, escapeNonASCII(string(data))).err != nil {
		return r.err
	}
	return r.b.NoContent(http.StatusOK)
}
