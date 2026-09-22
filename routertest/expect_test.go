package routertest_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

func expectRouter() *router.Router[*appContext] {
	r := router.New(newContext)
	r.GET("/json", func(c *appContext) error {
		return c.JSON(http.StatusOK, user{Name: "ann", Age: 30})
	})
	r.GET("/x", func(c *appContext) error {
		c.SetHeader("X-Who", "ann")
		return c.String(http.StatusOK, "hello")
	})
	r.GET("/moved", func(c *appContext) error {
		return c.Redirect(http.StatusSeeOther, "/done")
	})
	r.GET("/typed", func(c *appContext) error {
		c.SetHeader(router.HeaderContentType, c.Query("type"))
		return c.NoContent(http.StatusOK)
	})
	return r
}

func TestExpectPassesAMatchingResponse(t *testing.T) {
	r := expectRouter()

	tb := new(recordingTB)
	res := routertest.Get(r, "/json")
	e := res.Expect(tb)
	chain := []*routertest.Expect{
		e.Status(http.StatusOK),
		e.Body(`{"name":"ann","age":30}`),
		e.Contains(`"ann"`),
		e.NotContains("bob"),
		e.Header("X-Absent", ""),
		e.ContentType(router.MIMEApplicationJSON),
	}
	res = routertest.Get(r, "/moved")
	e2 := res.Expect(tb)
	chain = append(chain, e2.Redirect(http.StatusSeeOther, "/done"))

	if tb.failed {
		t.Fatalf("a matching response failed: fatals %q, errors %q", tb.fatals, tb.errors)
	}
	for i, got := range chain[:len(chain)-1] {
		if got != e {
			t.Errorf("check %d returned another *Expect", i)
		}
	}
	if chain[len(chain)-1] != e2 {
		t.Error("Redirect returned another *Expect")
	}
}

func TestExpectReportsEachMiss(t *testing.T) {
	tests := []struct {
		name   string
		target string
		check  func(*routertest.Expect)
		want   string
		fatal  bool
	}{
		{
			name: "status", target: "/x", fatal: true, want: "GET /x: status = 200, want 404; body: hello",
			check: func(e *routertest.Expect) { e.Status(http.StatusNotFound) },
		},
		{
			name: "body", target: "/x", want: `GET /x: body = "hello", want "bye"`,
			check: func(e *routertest.Expect) { e.Body("bye") },
		},
		{
			name: "contains", target: "/x", want: `does not contain "bye"; body: hello`,
			check: func(e *routertest.Expect) { e.Contains("bye") },
		},
		{
			name: "not contains", target: "/x", want: `body contains "ell"; body: hello`,
			check: func(e *routertest.Expect) { e.NotContains("ell") },
		},
		{
			name: "header", target: "/x", want: `header X-Who = "ann", want "bob"`,
			check: func(e *routertest.Expect) { e.Header("X-Who", "bob") },
		},
		{
			name: "content type", target: "/x", want: "want application/json",
			check: func(e *routertest.Expect) { e.ContentType(router.MIMEApplicationJSON) },
		},
		{
			name: "missing content type", target: "/typed", want: `Content-Type = "", want text/plain`,
			check: func(e *routertest.Expect) { e.ContentType(router.MIMETextPlain) },
		},
		{
			name: "malformed content type", target: "/typed?type=%3Bbroken", want: `Content-Type = ";broken"`,
			check: func(e *routertest.Expect) { e.ContentType(router.MIMETextPlain) },
		},
		{
			name: "redirect location", target: "/moved", want: `GET /moved: Location = "/done", want "/elsewhere"`,
			check: func(e *routertest.Expect) { e.Redirect(http.StatusSeeOther, "/elsewhere") },
		},
		{
			name: "field errors on a body that is not one", target: "/x", want: "decode the error body",
			check: func(e *routertest.Expect) { e.FieldErrors("email") },
		},
	}
	r := expectRouter()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tb := new(recordingTB)
			tt.check(routertest.Get(r, tt.target).Expect(tb))

			got := tb.errors
			if tt.fatal {
				got = tb.fatals
			}
			if len(tb.fatals)+len(tb.errors) != 1 || len(got) != 1 {
				t.Fatalf("fatals %q, errors %q; want one %s", tb.fatals, tb.errors, map[bool]string{true: "fatal", false: "error"}[tt.fatal])
			}
			if !strings.Contains(got[0], tt.want) {
				t.Errorf("message = %q, want it to contain %q", got[0], tt.want)
			}
		})
	}
}

func TestExpectRedirectWithTheWrongCodeIsFatal(t *testing.T) {
	tb := new(recordingTB)
	routertest.Get(expectRouter(), "/moved").Expect(tb).Redirect(http.StatusFound, "/done")
	if len(tb.fatals) != 1 {
		t.Errorf("fatals = %q, want one", tb.fatals)
	}
}

func TestExpectKeepsGoingAfterAMiss(t *testing.T) {
	tb := new(recordingTB)
	routertest.Get(expectRouter(), "/x").Expect(tb).Contains("a").Contains("b")
	if len(tb.errors) != 2 || len(tb.fatals) != 0 {
		t.Errorf("errors %q, fatals %q; want 2 errors and no fatal", tb.errors, tb.fatals)
	}
}

func TestExpectOnARecordedResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusTeapot)

	tb := new(recordingTB)
	routertest.Recorded(rec).Expect(tb).Status(http.StatusOK)
	if len(tb.fatals) != 1 || !strings.HasPrefix(tb.fatals[0], "status = 418") {
		t.Errorf("fatals = %q, want one without a request prefix", tb.fatals)
	}
}

func TestExpectContentTypeIgnoresParametersAndCase(t *testing.T) {
	tb := new(recordingTB)
	routertest.Get(expectRouter(), "/typed?type=Application/JSON%3B+charset=utf-8").
		Expect(tb).ContentType(router.MIMEApplicationJSON)
	if tb.failed {
		t.Errorf("errors = %q", tb.errors)
	}
}

func TestExpectContentTypeRejectsParameters(t *testing.T) {
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "Header") {
			t.Errorf("panic = %q, want one that points to Header", msg)
		}
	}()
	routertest.Get(expectRouter(), "/x").Expect(t).ContentType(router.MIMETextHTMLCharsetUTF8)
}

func TestExpectFieldErrors(t *testing.T) {
	r := jsonErrorRouter()
	res := routertest.Do(r, http.MethodPost, "/signup", routertest.JSONBody(signup{}))

	tb := new(recordingTB)
	res.Expect(tb).Status(http.StatusUnprocessableEntity).FieldErrors("email", "name")
	if tb.failed {
		t.Fatalf("errors = %q", tb.errors)
	}

	tb = new(recordingTB)
	res.Expect(tb).FieldErrors("email")
	if len(tb.errors) != 1 || !strings.Contains(tb.errors[0], "field errors = [") {
		t.Errorf("errors = %q, want one that lists the fields", tb.errors)
	}
}
