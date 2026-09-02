package router

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// HEAD is decided in its own place on every response path: Blob, String,
// Stream, RenderStream, the three error representations, the problem document,
// the SSE stream and the file server each test the method themselves, and so do
// the gzip middleware and the asset server in their own packages. The nil
// gzip.Writer crash came from exactly that spread. This is the one rule they all
// answer to:
//
//	A HEAD reply carries the status and the headers its GET would carry, and
//	no body.
//
// routertest.AssertHEADMatchesGET spells the same rule for the packages that
// cannot import this one, and the gzip middleware and the asset server answer
// it there. assertHEADMatchesGET below is that check again, because routertest
// imports this package.
type headCase struct {
	name   string
	setup  func(t *testing.T, r *Router[*tctx])
	target string
	header map[string]string
	// check guards the case itself. A case that stops reaching the path it
	// names would satisfy the contract for the wrong reason.
	check func(t *testing.T, get *httptest.ResponseRecorder)
}

func wantResponseHeader(name, value string) func(*testing.T, *httptest.ResponseRecorder) {
	return func(t *testing.T, get *httptest.ResponseRecorder) {
		t.Helper()
		if got := get.Header().Get(name); got != value {
			t.Fatalf("the GET sent %s: %q, want %q; this case no longer reaches the path it names", name, got, value)
		}
	}
}

func writeTempFile(t *testing.T, body string) (dir, name string) {
	t.Helper()
	dir, name = t.TempDir(), "asset.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, name
}

func headCases() []headCase {
	const body = "the body of the response"

	return []headCase{
		{
			name: "Blob",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error {
					return c.Blob(http.StatusOK, "application/octet-stream", []byte(body))
				})
			},
		},
		{
			name: "String",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error { return c.String(http.StatusOK, body) })
			},
		},
		{
			name: "HTML",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error { return c.HTML(http.StatusOK, "<p>"+body+"</p>") })
			},
		},
		{
			name: "JSON",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error {
					return c.JSON(http.StatusOK, map[string]string{"body": body})
				})
			},
		},
		{
			name: "Stream",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error {
					return c.Stream(http.StatusOK, "text/plain", strings.NewReader(body))
				})
			},
		},
		{
			name: "Render",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error {
					return c.Render(http.StatusOK, ComponentFunc(func(_ context.Context, w io.Writer) error {
						_, err := io.WriteString(w, body)
						return err
					}))
				})
			},
		},
		{
			name: "RenderStream",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error {
					return c.RenderStream(http.StatusOK, ComponentFunc(func(_ context.Context, w io.Writer) error {
						_, err := io.WriteString(w, body)
						return err
					}))
				})
			},
		},
		{
			name: "Attachment",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error {
					return c.Attachment(http.StatusOK, "text/plain", "note.txt", []byte(body))
				})
			},
			check: wantResponseHeader(HeaderContentDisposition, `attachment; filename="note.txt"`),
		},
		{
			name: "NoContent",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error { return c.NoContent(http.StatusNoContent) })
			},
		},
		{
			name: "Redirect",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error { return c.Redirect(http.StatusFound, "/elsewhere") })
			},
			check: wantResponseHeader(HeaderLocation, "/elsewhere"),
		},
		{
			name: "File",
			setup: func(t *testing.T, r *Router[*tctx]) {
				dir, name := writeTempFile(t, body)
				r.GET("/x", func(c *tctx) error { return c.FileFS(name, os.DirFS(dir)) })
			},
			check: wantResponseHeader("Accept-Ranges", "bytes"),
		},
		{
			name: "an error as plain text",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(*tctx) error { return ErrBadRequest })
			},
			header: map[string]string{HeaderAccept: MIMETextPlain},
			check:  wantResponseHeader(HeaderContentType, MIMETextPlainCharsetUTF8),
		},
		{
			name: "an SSE stream",
			setup: func(_ *testing.T, r *Router[*tctx]) {
				r.GET("/x", func(c *tctx) error {
					s, err := c.SSE(http.StatusOK)
					if err != nil {
						return err
					}
					return s.Send(Event{Data: body})
				})
			},
			check: wantResponseHeader(HeaderContentType, MIMETextEventStream),
		},
		{
			name:   "no route",
			setup:  func(_ *testing.T, r *Router[*tctx]) { r.GET("/other", noContent) },
			target: "/missing",
		},
		{
			name:  "no such method",
			setup: func(_ *testing.T, r *Router[*tctx]) { r.POST("/x", noContent) },
			check: wantResponseHeader(HeaderAllow, "OPTIONS, POST"),
		},
	}
}

func noContent(c *tctx) error { return c.NoContent(http.StatusNoContent) }

func TestHEADCarriesTheHeadersOfItsGETAndNoBody(t *testing.T) {
	for _, tc := range headCases() {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			if target == "" {
				target = "/x"
			}
			// A fresh router per request: nothing carries over from the GET.
			answer := func(method string) *httptest.ResponseRecorder {
				r := newTestRouter()
				tc.setup(t, r)
				req := httptest.NewRequest(method, target, nil)
				for name, value := range tc.header {
					req.Header.Set(name, value)
				}
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)
				return rec
			}

			get, head := answer(http.MethodGet), answer(http.MethodHead)
			if tc.check != nil {
				tc.check(t, get)
			}
			assertHEADMatchesGET(t, get, head)
		})
	}
}

func assertHEADMatchesGET(t *testing.T, get, head *httptest.ResponseRecorder) {
	t.Helper()
	if head.Code != get.Code {
		t.Errorf("status = %d, want the %d of the GET", head.Code, get.Code)
	}
	if head.Body.Len() != 0 {
		t.Errorf("body = %q, want none", head.Body.String())
	}
	if !maps.EqualFunc(head.Header(), get.Header(), slices.Equal) {
		t.Errorf("headers = %v, want the %v of the GET", head.Header(), get.Header())
	}
}
