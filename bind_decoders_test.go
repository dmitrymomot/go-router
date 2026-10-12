package router

import (
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type xmlOrder struct {
	XMLName xml.Name `xml:"order"`
	Item    string   `xml:"item"`
	Qty     int      `xml:"qty"`
	// Tagged for the body and the path: the path wins, the body may fill it.
	Shop string `xml:"shop" param:"shop"`
	// Tagged for a header and not for the body: the body must not fill it.
	Tenant string `header:"X-Tenant"`
}

func xmlRouter() *Router[*tctx] {
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.Decoder("application/xml", func(r io.Reader, v any) error { return xml.NewDecoder(r).Decode(v) })
	r.POST("/{shop}/orders", func(c *tctx) error {
		in, err := c.Bind[xmlOrder]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, in.Item+"/"+strconv.Itoa(in.Qty)+"/"+in.Shop+"/"+in.Tenant)
	})
	return r
}

func TestBindUsesARegisteredDecoder(t *testing.T) {
	r := xmlRouter()
	body := `<order><item>tea</item><qty>2</qty><shop>evil</shop><Tenant>evil</Tenant></order>`
	for _, ct := range []string{"application/xml", "Application/XML; charset=utf-8"} {
		rec := post(r, "/acme/orders", ct, body)
		if rec.Code != http.StatusOK || rec.Body.String() != "tea/2/acme/" {
			t.Errorf("%s: %d %q, want 200 tea/2/acme/", ct, rec.Code, rec.Body.String())
		}
	}
	if code := post(r, "/acme/orders", "text/xml", body).Code; code != http.StatusUnsupportedMediaType {
		t.Errorf("an unregistered type: status = %d, want 415", code)
	}
}

func TestBindReportsTheErrorOfARegisteredDecoder(t *testing.T) {
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.MaxBodyBytes(64)
	r.Decoder("application/xml", func(r io.Reader, v any) error { return xml.NewDecoder(r).Decode(v) })
	r.Decoder("application/x-teapot", func(io.Reader, any) error { return ErrConflict.WithMessage("busy") })
	r.POST("/", func(c *tctx) error {
		_, err := c.Bind[xmlOrder]()
		return err
	})
	for _, tt := range []struct {
		ct, body string
		code     int
		want     string
	}{
		{"application/xml", "<order><item>", http.StatusBadRequest, "malformed application/xml body"},
		{"application/xml", "<order>" + strings.Repeat("x", 100) + "</order>", http.StatusRequestEntityTooLarge, ""},
		{"application/x-teapot", "x", http.StatusConflict, "busy"},
	} {
		rec := post(r, "/", tt.ct, tt.body)
		if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s %.12q: %d %q, want %d %q", tt.ct, tt.body, rec.Code, rec.Body.String(), tt.code, tt.want)
		}
	}
}

func TestOwnTag(t *testing.T) {
	for mt, want := range map[string]string{
		"application/xml":          "xml",
		"text/xml":                 "xml",
		"application/atom+xml":     "xml",
		"application/x-msgpack":    "msgpack",
		"application/cbor":         "cbor",
		"application/x-protobuf":   "protobuf",
		"application/vnd.api+yaml": "yaml",
	} {
		if got := ownTag(mt); got != want {
			t.Errorf("ownTag(%q) = %q, want %q", mt, got, want)
		}
	}
}

func TestDecoderPanicsOnBadInput(t *testing.T) {
	dec := func(io.Reader, any) error { return nil }
	for _, tt := range []struct {
		name, mt string
		d        BodyDecoder
		want     string
	}{
		{"nil", "application/xml", nil, "nil BodyDecoder"},
		{"no subtype", "xml", dec, "pass one type and subtype"},
		{"a wildcard", "application/*", dec, "without a wildcard"},
		{"parameters", "application/xml; charset=utf-8", dec, "with parameters"},
		{"malformed", "application/xml;;", dec, "does not parse"},
		{"empty", "", dec, "does not parse"},
		{"JSON", "application/json", dec, "built-in decoder of application/json"},
		{"a +json type", "application/problem+json", dec, "built-in decoder"},
		{"a form", "Application/X-WWW-Form-Urlencoded", dec, "built-in decoder"},
		{"multipart", "multipart/form-data", dec, "built-in decoder"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mustPanicContaining(t, tt.want, func() { newTestRouter().Decoder(tt.mt, tt.d) })
		})
	}

	t.Run("a scope", func(t *testing.T) {
		r := newTestRouter()
		mustPanicContaining(t, "belongs to the root router", func() { r.With().Decoder("application/xml", dec) })
	})
	t.Run("after serving", func(t *testing.T) {
		r := newTestRouter()
		r.GET("/", func(c *tctx) error { return nil })
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		mustPanicContaining(t, "after the router started serving", func() { r.Decoder("application/xml", dec) })
	})
}

func TestDecoderReplacesAnEarlierOne(t *testing.T) {
	r := newTestRouter()
	r.Decoder("application/xml", func(io.Reader, any) error { return errors.New("first") })
	r.Decoder("application/xml", func(io.Reader, any) error { return nil })
	r.POST("/", func(c *tctx) error {
		_, err := c.Bind[xmlOrder]()
		return err
	})
	if code := post(r, "/", "application/xml", "<order/>").Code; code != http.StatusOK {
		t.Errorf("status = %d, want 200 from the second decoder", code)
	}
}
