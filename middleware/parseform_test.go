package middleware_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func parseFormRouter(ran *int, mw ...router.Middleware[*appContext]) *router.Router[*appContext] {
	r := newRouter()
	r.MaxBodyBytes(16)
	r.Use(mw...)
	r.Use(middleware.ParseForm[*appContext])
	r.POST("/toggle", func(c *appContext) error {
		*ran++
		return c.String(http.StatusOK, fmt.Sprintf("%v", c.FormValue("on") == "on"))
	})
	return r
}

func postBody(target, contentType, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set(router.HeaderContentType, contentType)
	}
	return req
}

// Without ParseForm, the handler reads an oversized form as empty, and so a
// checkbox as off, and acts on it.
func TestParseFormRefusesAnOversizedForm(t *testing.T) {
	ran := 0
	r := parseFormRouter(&ran)

	rec := do(r, postBody("/toggle", router.MIMEApplicationForm, "on=on&pad="+strings.Repeat("x", 100)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
	if ran != 0 {
		t.Error("the handler ran for a form over the cap")
	}
}

func TestParseFormRefusesAMalformedMultipartBody(t *testing.T) {
	ran := 0
	r := parseFormRouter(&ran)

	rec := do(r, postBody("/toggle", "multipart/form-data; boundary=B", "not multipart"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if ran != 0 {
		t.Error("the handler ran for a malformed body")
	}
}

func TestParseFormPlainFormRefusesAMalformedBody(t *testing.T) {
	ran := 0
	r := parseFormRouter(&ran)

	rec := do(r, postBody("/toggle", router.MIMEApplicationForm, "on=%zz"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if ran != 0 {
		t.Error("the handler ran for a malformed body")
	}
}

func TestParseFormHandsTheHandlerTheParsedForm(t *testing.T) {
	ran := 0
	r := parseFormRouter(&ran)

	rec := do(r, postBody("/toggle", router.MIMEApplicationForm, "on=on"))
	if rec.Code != http.StatusOK || rec.Body.String() != "true" {
		t.Errorf("response = %d %q, want 200 %q", rec.Code, rec.Body.String(), "true")
	}
}

func TestParseFormLeavesAJSONBodyAlone(t *testing.T) {
	r := newRouter()
	r.MaxBodyBytes(16)
	r.Use(middleware.ParseForm[*appContext])
	r.POST("/raw", func(c *appContext) error {
		b, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d", len(b)))
	})

	rec := do(r, postBody("/raw", router.MIMEApplicationJSON, strings.Repeat("x", 100)))
	if rec.Code != http.StatusOK || rec.Body.String() != "100" {
		t.Errorf("response = %d %q, want 200 %q", rec.Code, rec.Body.String(), "100")
	}
}

func TestParseFormPassesARequestWithoutABody(t *testing.T) {
	ran := 0
	r := parseFormRouter(&ran)

	req := httptest.NewRequest(http.MethodPost, "/toggle", nil)
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationForm)
	if rec := do(r, req); rec.Code != http.StatusOK || ran != 1 {
		t.Errorf("status = %d, ran %d times, want 200 once", rec.Code, ran)
	}

	req = httptest.NewRequest(http.MethodPost, "/toggle", nil)
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationForm)
	req.Body = nil
	if rec := do(r, req); rec.Code != http.StatusOK || ran != 2 {
		t.Errorf("nil body: status = %d, ran %d times, want 200 twice", rec.Code, ran)
	}
}

func TestParseFormReadsTheContentTypeCaseInsensitively(t *testing.T) {
	ran := 0
	r := parseFormRouter(&ran)

	rec := do(r, postBody("/toggle", " Application/X-WWW-Form-URLEncoded ; charset=utf-8",
		"on=on&pad="+strings.Repeat("x", 100)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
	if ran != 0 {
		t.Error("the handler ran for a form over the cap")
	}
}

func TestParseFormTakesTheCapOfABodyLimitInFront(t *testing.T) {
	ran := 0
	r := parseFormRouter(&ran, middleware.BodyLimit[*appContext](1<<10))

	rec := do(r, postBody("/toggle", router.MIMEApplicationForm, "on=on&pad="+strings.Repeat("x", 100)))
	if rec.Code != http.StatusOK || rec.Body.String() != "true" {
		t.Errorf("response = %d %q, want 200 %q", rec.Code, rec.Body.String(), "true")
	}
}

func TestParseFormIgnoresAMalformedQuery(t *testing.T) {
	ran := 0
	r := parseFormRouter(&ran)

	rec := do(r, postBody("/toggle?q=%zz", router.MIMEApplicationForm, "on=on"))
	if rec.Code != http.StatusOK || rec.Body.String() != "true" {
		t.Errorf("response = %d %q, want 200 %q", rec.Code, rec.Body.String(), "true")
	}
}

func TestParseFormSkip(t *testing.T) {
	r := newRouter()
	r.MaxBodyBytes(16)
	r.Use(middleware.ParseFormWithConfig[*appContext](middleware.ParseFormConfig{
		Skip: skipPath("/stream"),
	}))
	r.POST("/stream", func(c *appContext) error {
		mr, err := c.Request().MultipartReader()
		if err != nil {
			return err
		}
		part, err := mr.NextPart()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, part.FormName())
	})

	body := "--B\r\nContent-Disposition: form-data; name=\"doc\"\r\n\r\n" +
		strings.Repeat("x", 100) + "\r\n--B--\r\n"
	rec := do(r, postBody("/stream", "multipart/form-data; boundary=B", body))
	if rec.Code != http.StatusOK || rec.Body.String() != "doc" {
		t.Errorf("response = %d %q, want 200 %q", rec.Code, rec.Body.String(), "doc")
	}
}

// hidingContext answers every Value lookup itself, so router.FromContext
// cannot find its Base.
type hidingContext struct {
	router.Base
}

func (*hidingContext) Value(any) any { return nil }

func TestParseFormPassesAContextThatHidesItsBase(t *testing.T) {
	r := router.New(func(http.ResponseWriter, *http.Request) *hidingContext { return new(hidingContext) })
	r.Use(middleware.ParseForm[*hidingContext])
	r.POST("/", func(c *hidingContext) error { return c.NoContent(http.StatusNoContent) })

	if rec := do(r, postBody("/", router.MIMEApplicationForm, "on=%zz")); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
