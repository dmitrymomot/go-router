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

const (
	HeaderHXRequest = "Hx-Request"

	HeaderHXBoosted = "Hx-Boosted"

	HeaderHXCurrentURL = "Hx-Current-Url"

	HeaderHXHistoryRestoreRequest = "Hx-History-Restore-Request"

	HeaderHXPrompt = "Hx-Prompt"

	HeaderHXTarget = "Hx-Target"

	HeaderHXTriggerName = "Hx-Trigger-Name"

	HeaderHXTrigger = "Hx-Trigger"
)

const (
	HeaderHXLocation = "Hx-Location"

	HeaderHXPushURL = "Hx-Push-Url"

	HeaderHXRedirect = "Hx-Redirect"

	HeaderHXRefresh = "Hx-Refresh"

	HeaderHXReplaceURL = "Hx-Replace-Url"

	HeaderHXReswap = "Hx-Reswap"

	HeaderHXRetarget = "Hx-Retarget"

	HeaderHXReselect = "Hx-Reselect"

	HeaderHXTriggerAfterSettle = "Hx-Trigger-After-Settle"

	HeaderHXTriggerAfterSwap = "Hx-Trigger-After-Swap"
)

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

// betteralign:check
type HTMXRequest struct {
	CurrentURL string

	Prompt string

	Target string

	Trigger string

	TriggerName string

	Request bool

	Boosted bool

	HistoryRestore bool
}

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

func (b *Base) IsHTMX() bool { return hxTrue(b.req.Header.Get(HeaderHXRequest)) }

func (b *Base) IsBoosted() bool { return hxTrue(b.req.Header.Get(HeaderHXBoosted)) }

func HTMXWantsPartial(r *http.Request) bool {
	h := r.Header
	return hxTrue(h.Get(HeaderHXRequest)) &&
		!hxTrue(h.Get(HeaderHXBoosted)) &&
		!hxTrue(h.Get(HeaderHXHistoryRestoreRequest))
}

func hxTrue(v string) bool { return strings.EqualFold(v, "true") }

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

type HXEvent struct {
	Detail any

	Name string
}

type HXLocation struct {
	Path string `json:"path"`

	Target string `json:"target,omitzero"`

	Swap string `json:"swap,omitzero"`

	Select string `json:"select,omitzero"`

	Source string `json:"source,omitzero"`

	Event string `json:"event,omitzero"`

	Handler string `json:"handler,omitzero"`

	Headers map[string]string `json:"headers,omitzero"`

	Values map[string]string `json:"values,omitzero"`
}

func (l HXLocation) isPathOnly() bool {
	return l.Target == "" && l.Swap == "" && l.Select == "" && l.Source == "" &&
		l.Event == "" && l.Handler == "" && len(l.Headers) == 0 && len(l.Values) == 0
}

type HXResponse struct {
	b *Base
}

func (b *Base) HX() HXResponse { return HXResponse{b: b} }

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

func (h HXResponse) PushURL(url string) HXResponse {
	return h.set(HeaderHXPushURL, url)
}

func (h HXResponse) ReplaceURL(url string) HXResponse {
	return h.set(HeaderHXReplaceURL, url)
}

func (h HXResponse) Retarget(selector string) HXResponse {
	return h.set(HeaderHXRetarget, selector)
}

func (h HXResponse) Reselect(selector string) HXResponse {
	return h.set(HeaderHXReselect, selector)
}

func (h HXResponse) Reswap(swap string) HXResponse {
	return h.set(HeaderHXReswap, swap)
}

func (h HXResponse) Refresh() HXResponse {
	return h.set(HeaderHXRefresh, "true")
}

func (h HXResponse) Trigger(names ...string) HXResponse {
	return h.triggerNames(HeaderHXTrigger, names)
}

func (h HXResponse) TriggerAfterSwap(names ...string) HXResponse {
	return h.triggerNames(HeaderHXTriggerAfterSwap, names)
}

func (h HXResponse) TriggerAfterSettle(names ...string) HXResponse {
	return h.triggerNames(HeaderHXTriggerAfterSettle, names)
}

func (h HXResponse) TriggerEvents(events ...HXEvent) HXResponse {
	return h.triggerEvents(HeaderHXTrigger, events)
}

func (h HXResponse) TriggerEventsAfterSwap(events ...HXEvent) HXResponse {
	return h.triggerEvents(HeaderHXTriggerAfterSwap, events)
}

func (h HXResponse) TriggerEventsAfterSettle(events ...HXEvent) HXResponse {
	return h.triggerEvents(HeaderHXTriggerAfterSettle, events)
}

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

func (h HXResponse) triggerEvents(header string, events []HXEvent) HXResponse {
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

func (h HXResponse) Render(status int, c Component) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.Render(status, c)
}

func (h HXResponse) RenderStream(status int, c Component) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.RenderStream(status, c)
}

func (h HXResponse) HTML(status int, html string) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.HTML(status, html)
}

func (h HXResponse) String(status int, s string) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.String(status, s)
}

func (h HXResponse) JSON(status int, v any, opts ...json.Options) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.JSON(status, v, opts...)
}

func (h HXResponse) NoContent(status int) error {
	if err := h.b.hxError(); err != nil {
		return err
	}
	return h.b.NoContent(status)
}

func (h HXResponse) NoSwap() error { return h.NoContent(http.StatusNoContent) }

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
