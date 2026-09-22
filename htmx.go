package router

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

// The htmx headers that a request carries. [Base.HTMX] reads them into an
// [HTMXRequest]. They are the headers htmx 4 sends; htmx 2 is not supported.
const (
	HeaderHXRequest               = "Hx-Request"
	HeaderHXRequestType           = "Hx-Request-Type"
	HeaderHXBoosted               = "Hx-Boosted"
	HeaderHXCurrentURL            = "Hx-Current-Url"
	HeaderHXHistoryRestoreRequest = "Hx-History-Restore-Request"
	HeaderHXSource                = "Hx-Source"
	HeaderHXTarget                = "Hx-Target"
)

// The htmx headers that a response carries. [Base.HX] sets them.
const (
	HeaderHXLocation   = "Hx-Location"
	HeaderHXPushURL    = "Hx-Push-Url"
	HeaderHXRedirect   = "Hx-Redirect"
	HeaderHXRefresh    = "Hx-Refresh"
	HeaderHXReplaceURL = "Hx-Replace-Url"
	HeaderHXReswap     = "Hx-Reswap"
	HeaderHXRetarget   = "Hx-Retarget"
	HeaderHXReselect   = "Hx-Reselect"
	HeaderHXTrigger    = "Hx-Trigger"
)

// The swap styles that [HXResponse.Reswap] takes.
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

// HTMXRequest is what the htmx headers of one request say. Request is false
// when htmx did not make the request, and the other fields are then empty.
//
// RequestType is "full" when htmx swaps the whole body or selects part of the
// answer, as for a boosted link or a history restore, and "partial" when it
// swaps one element; it is what [Base.WantsPartial] reads. Source names the
// element that made the request and Target the one the answer goes into, both
// in the tag#id form htmx 4 sends, such as "ul#user-list".
// [HTMXRequest.TargetID] and [HTMXRequest.SourceID] read the id alone.
//
// htmx 4 swaps a 4xx or 5xx answer into the target too, so a scope that serves
// htmx wants an error handler that renders a fragment from [HTTPErrorOf].
//
//betteralign:check
type HTMXRequest struct {
	CurrentURL     string
	RequestType    string
	Source         string
	Target         string
	Request        bool
	Boosted        bool
	HistoryRestore bool
}

// HTMX reads the htmx headers of the request.
func (b *Base) HTMX() HTMXRequest {
	h := b.req.Header
	return HTMXRequest{
		CurrentURL:     h.Get(HeaderHXCurrentURL),
		RequestType:    h.Get(HeaderHXRequestType),
		Source:         h.Get(HeaderHXSource),
		Target:         h.Get(HeaderHXTarget),
		Request:        hxTrue(h.Get(HeaderHXRequest)),
		Boosted:        hxTrue(h.Get(HeaderHXBoosted)),
		HistoryRestore: hxTrue(h.Get(HeaderHXHistoryRestoreRequest)),
	}
}

// TargetID reports the id of the element the answer goes into: "user-list"
// for the target "ul#user-list". It is "" when the target has no id.
func (h HTMXRequest) TargetID() string { return idOf(h.Target) }

// SourceID reports the id of the element that made the request: "delete-7"
// for the source "button#delete-7". It is "" when the source has no id.
func (h HTMXRequest) SourceID() string { return idOf(h.Source) }

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

// IsHTMX reports whether htmx made the request. It says who sent the
// request, not what to answer: a boosted link and a history restore come from
// htmx too and want a whole page. [Base.WantsPartial] decides that.
func (b *Base) IsHTMX() bool { return hxTrue(b.req.Header.Get(HeaderHXRequest)) }

// IsBoosted reports whether the request comes from an hx-boost link or form.
// htmx 4 sends a boosted link that swaps the body as a full request, so
// [Base.WantsPartial] answers it with the page; a boosted element that targets
// one element is partial.
func (b *Base) IsBoosted() bool { return hxTrue(b.req.Header.Get(HeaderHXBoosted)) }

// HTMXWantsPartial reports whether r wants a fragment: htmx made it, and its
// HX-Request-Type is not "full". htmx 4 sends "full" for a boosted link, a
// history restore, and a request that swaps the whole body or selects part of
// the answer. A request without the type counts as partial. htmx 2 sends no
// type, so it is not supported: its boosted links and history restores would
// get fragments.
func HTMXWantsPartial(r *http.Request) bool {
	h := r.Header
	return hxTrue(h.Get(HeaderHXRequest)) && !strings.EqualFold(h.Get(HeaderHXRequestType), "full")
}

func hxTrue(v string) bool { return strings.EqualFold(v, "true") }

// hxVary lists the headers that [HTMXWantsPartial] reads. The HTMXRedirect
// middleware names the same two, in the same order.
var hxVary = []string{HeaderHXRequest, HeaderHXRequestType}

// WantsPartial reports whether the request wants a fragment rather than a
// whole page, as [HTMXWantsPartial] decides. It adds the headers it reads to
// Vary, so a cache keeps the two answers apart.
func (b *Base) WantsPartial() bool {
	b.Vary(hxVary...)
	return HTMXWantsPartial(b.req)
}

// RenderPartial renders partial with status for a request that wants a
// fragment, and page for any other. See [Base.WantsPartial] and [Base.Render].
func (b *Base) RenderPartial(status int, partial, page Component) error {
	if b.WantsPartial() {
		return b.Render(status, partial)
	}
	return b.Render(status, page)
}

// HTMXPartial picks between two handlers for one route: partial for a request
// that wants a fragment, page for anything else. It adds the htmx headers to
// Vary, so a cache keeps the two answers apart.
//
// HTMXPartial panics if either handler is nil.
func HTMXPartial[C Context](partial, page HandlerFunc[C]) HandlerFunc[C] {
	if partial == nil || page == nil {
		panic("router: HTMXPartial needs both handlers")
	}
	return func(c C) error {
		if c.base().WantsPartial() {
			return partial(c)
		}
		return page(c)
	}
}

// HXEvent is one client-side event, with a detail that goes out as JSON. See
// [HXResponse.TriggerEvents].
type HXEvent struct {
	Detail any
	Name   string
}

// HXLocation is a client-side navigation with the details of the swap. Path is
// the only required field. See [HXResponse.LocationWith].
type HXLocation struct {
	Path    string            `json:"path"`
	Target  string            `json:"target,omitzero"`
	Swap    string            `json:"swap,omitzero"`
	Select  string            `json:"select,omitzero"`
	Source  string            `json:"source,omitzero"`
	Event   string            `json:"event,omitzero"`
	Headers map[string]string `json:"headers,omitzero"`
	Values  map[string]string `json:"values,omitzero"`
}

func (l HXLocation) isPathOnly() bool {
	return l.Target == "" && l.Swap == "" && l.Select == "" && l.Source == "" &&
		l.Event == "" && len(l.Headers) == 0 && len(l.Values) == 0
}

// HXResponse builds an htmx answer. The header methods chain, and one of the
// answer methods ends the chain.
//
// A method that is handed something a header cannot carry records the failure
// and writes nothing. Every later method in the chain then does nothing, and
// the answer method reports the failure, so a chain needs one error check at
// its end and not one per call. [HXResponse.Err] reads the failure early.
type HXResponse struct {
	b *Base
}

// HX opens an htmx answer. See [HXResponse].
func (b *Base) HX() HXResponse { return HXResponse{b: b} }

// Err reports the failure that the chain recorded, or nil.
func (h HXResponse) Err() error { return h.b.hxError() }

func (h HXResponse) fail(err error) HXResponse {
	h.b.setHXError(ErrInternalServerError.WithError(err))
	return h
}

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

// PushURL puts url in the history of the browser.
func (h HXResponse) PushURL(url string) HXResponse {
	return h.set(HeaderHXPushURL, url)
}

// ReplaceURL replaces the current entry in the history of the browser with
// url.
func (h HXResponse) ReplaceURL(url string) HXResponse {
	return h.set(HeaderHXReplaceURL, url)
}

// Retarget swaps the answer into the element that selector names, in place of
// the one the request targeted.
func (h HXResponse) Retarget(selector string) HXResponse {
	return h.set(HeaderHXRetarget, selector)
}

// Reselect takes the part of the answer that selector names, in place of the
// whole body.
func (h HXResponse) Reselect(selector string) HXResponse {
	return h.set(HeaderHXReselect, selector)
}

// Reswap changes how the answer is swapped in. See the HXSwap constants.
func (h HXResponse) Reswap(swap string) HXResponse {
	return h.set(HeaderHXReswap, swap)
}

// Refresh tells the client to reload the whole page.
func (h HXResponse) Refresh() HXResponse {
	return h.set(HeaderHXRefresh, "true")
}

// Trigger fires the named events on the element that made the request, once
// htmx has swapped the answer in. The events bubble. A name has to be ASCII,
// and it cannot be empty, hold a comma or a line break, or repeat; use
// [HXResponse.TriggerEvents] for anything else.
func (h HXResponse) Trigger(names ...string) HXResponse {
	if h.b.hxError() != nil || len(names) == 0 {
		return h
	}
	for i, n := range names {
		if err := validEventName(n); err != nil {
			return h.fail(fmt.Errorf(
				"router: the %s header cannot carry the event name %q: %w", HeaderHXTrigger, n, err))
		}
		if slices.Contains(names[:i], n) {
			return h.fail(fmt.Errorf(
				"router: the %s header names the event %q twice", HeaderHXTrigger, n))
		}
	}
	return h.set(HeaderHXTrigger, strings.Join(names, ", "))
}

// TriggerEvents is [HXResponse.Trigger] with a detail for each event, sent as
// JSON. A name outside ASCII is escaped, so any name that is not empty and
// does not repeat works here.
func (h HXResponse) TriggerEvents(events ...HXEvent) HXResponse {
	if h.b.hxError() != nil || len(events) == 0 {
		return h
	}

	var seen map[string]bool
	if len(events) > 1 {
		seen = make(map[string]bool, len(events))
	}

	var sb strings.Builder
	sb.WriteByte('{')
	for i, e := range events {
		if e.Name == "" {
			return h.fail(fmt.Errorf("router: the %s header holds an event without a name", HeaderHXTrigger))
		}
		if seen != nil {
			if seen[e.Name] {
				return h.fail(fmt.Errorf(
					"router: the %s header names the event %q twice", HeaderHXTrigger, e.Name))
			}
			seen[e.Name] = true
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		name, err := json.Marshal(e.Name, h.b.headerJSONOptions()...)
		if err != nil {
			return h.fail(fmt.Errorf(
				"router: encode the name of the %s event %q: %w", HeaderHXTrigger, e.Name, err))
		}
		sb.Write(name)
		sb.WriteByte(':')

		detail, err := json.Marshal(e.Detail, h.b.headerJSONOptions()...)
		if err != nil {
			return h.fail(fmt.Errorf(
				"router: encode the detail of the %s event %q: %w", HeaderHXTrigger, e.Name, err))
		}
		sb.Write(detail)
	}
	sb.WriteByte('}')
	return h.set(HeaderHXTrigger, escapeNonASCII(sb.String()))
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

// Render ends the chain with [Base.Render], or reports the failure the chain
// recorded.
func (h HXResponse) Render(status int, c Component) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.Render(status, c)
}

// RenderStream ends the chain with [Base.RenderStream], or reports the failure
// the chain recorded.
func (h HXResponse) RenderStream(status int, c Component) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.RenderStream(status, c)
}

// HTML ends the chain with [Base.HTML], or reports the failure the chain
// recorded.
func (h HXResponse) HTML(status int, html string) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.HTML(status, html)
}

// String ends the chain with [Base.String], or reports the failure the chain
// recorded.
func (h HXResponse) String(status int, s string) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.String(status, s)
}

// JSON ends the chain with [Base.JSON], or reports the failure the chain
// recorded.
func (h HXResponse) JSON(status int, v any, opts ...json.Options) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.JSON(status, v, opts...)
}

// NoContent ends the chain with status and no body, or reports the failure the
// chain recorded.
func (h HXResponse) NoContent(status int) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.NoContent(status)
}

// NoSwap answers 204, which tells htmx to swap nothing. The headers of the
// chain still reach the client.
func (h HXResponse) NoSwap() error { return h.NoContent(http.StatusNoContent) }

// clientNavigate sets one of the htmx navigation headers and answers 200, or
// falls back to an ordinary redirect for a request htmx did not make.
func (h HXResponse) clientNavigate(header, target string) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	if !h.b.IsHTMX() {
		return h.b.Redirect(http.StatusSeeOther, target)
	}
	h = h.set(header, target)
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.NoContent(http.StatusOK)
}

// Redirect sends the browser to url as a whole-page load. A request that htmx
// did not make gets an ordinary 303 instead.
func (h HXResponse) Redirect(url string) error {
	return h.clientNavigate(HeaderHXRedirect, url)
}

// Location sends the client to path as a swap, which keeps the page loaded. A
// request that htmx did not make gets an ordinary 303 instead.
func (h HXResponse) Location(path string) error {
	return h.clientNavigate(HeaderHXLocation, path)
}

// LocationWith is [HXResponse.Location] with the details of the swap. It
// reports a failure when loc names no path.
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

func (b *Base) headerJSONOptions() []json.Options {
	return b.jsonOptions([]json.Options{jsontext.Multiline(false)})
}
