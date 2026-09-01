package router

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type wrapKey struct{}

func comp(s string) ComponentFunc {
	return func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	}
}

func TestRender(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error { return c.Render(http.StatusOK, comp("<h1>hi</h1>")) })

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<h1>hi</h1>" {
		t.Errorf("body = %q", got)
	}
	if got := rec.Header().Get(HeaderContentType); got != MIMETextHTMLCharsetUTF8 {
		t.Errorf("Content-Type = %q, want %q", got, MIMETextHTMLCharsetUTF8)
	}
	if got := rec.Header().Get(HeaderContentLength); got != "11" {
		t.Errorf("Content-Length = %q, want %q", got, "11")
	}
}

func TestRenderKeepsTheHandlerContentType(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		c.SetHeader(HeaderContentType, "application/xhtml+xml")
		return c.Render(http.StatusOK, comp("<p/>"))
	})

	if got := do(r, http.MethodGet, "/").Header().Get(HeaderContentType); got != "application/xhtml+xml" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestRenderPassesTheContext(t *testing.T) {
	r := newTestRouter()
	r.GET("/u/{id}", func(c *tctx) error {
		c.Set("flash", "saved")
		return c.Render(http.StatusOK, ComponentFunc(func(ctx context.Context, w io.Writer) error {
			ctx = context.WithValue(ctx, wrapKey{}, "wrapped")
			b, ok := FromContext(ctx)
			if !ok {
				return io.ErrUnexpectedEOF
			}
			_, err := io.WriteString(w, ctx.Value("flash").(string)+" "+b.Param("id"))
			return err
		}))
	})

	if got := do(r, http.MethodGet, "/u/7").Body.String(); got != "saved 7" {
		t.Errorf("body = %q, want %q", got, "saved 7")
	}
}

func TestRenderErrorWritesNoPartialBody(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.Render(http.StatusOK, ComponentFunc(func(_ context.Context, w io.Writer) error {
			if _, err := io.WriteString(w, "<html>partial"); err != nil {
				return err
			}
			return io.ErrUnexpectedEOF
		}))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "partial") {
		t.Errorf("body leaked the partial page: %q", rec.Body.String())
	}
}

func TestRenderHEADWritesTheLengthOnly(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error { return c.Render(http.StatusOK, comp("<h1>hi</h1>")) })

	rec := do(r, http.MethodHead, "/")
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if got := rec.Header().Get(HeaderContentLength); got != "11" {
		t.Errorf("Content-Length = %q, want %q", got, "11")
	}
}

func TestRenderLargePage(t *testing.T) {
	want := strings.Repeat("x", maxPooledRenderBuf+1024)
	r := newTestRouter()
	r.GET("/", func(c *tctx) error { return c.Render(http.StatusOK, comp(want)) })

	if got := do(r, http.MethodGet, "/").Body.String(); got != want {
		t.Errorf("body length = %d, want %d", len(got), len(want))
	}
}

func TestRenderIsConcurrencySafe(t *testing.T) {
	r := newTestRouter()
	r.GET("/{tag}", func(c *tctx) error {
		return c.Render(http.StatusOK, comp(strings.Repeat(c.Param("tag"), 512)))
	})

	var wg sync.WaitGroup
	for tag := range 16 {
		want := strings.Repeat(string(rune('a'+tag)), 512)
		wg.Go(func() {
			for range 32 {
				if got := do(r, http.MethodGet, "/"+string(rune('a'+tag))).Body.String(); got != want {
					t.Errorf("body = %q, want %q", got, want)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestRenderStream(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.RenderStream(http.StatusAccepted, ComponentFunc(func(_ context.Context, w io.Writer) error {
			if _, err := io.WriteString(w, "a"); err != nil {
				return err
			}
			w.(*Response).Flush()
			_, err := io.WriteString(w, "b")
			return err
		}))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if got := rec.Body.String(); got != "ab" {
		t.Errorf("body = %q, want %q", got, "ab")
	}
	if !rec.Flushed {
		t.Error("the component did not reach the flusher")
	}
	if got := rec.Header().Get(HeaderContentType); got != MIMETextHTMLCharsetUTF8 {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestRenderStreamErrorKeepsTheStatus(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.RenderStream(http.StatusOK, ComponentFunc(func(_ context.Context, w io.Writer) error {
			if _, err := io.WriteString(w, "<html>"); err != nil {
				return err
			}
			return io.ErrUnexpectedEOF
		}))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<html>" {
		t.Errorf("body = %q, want %q", got, "<html>")
	}
}

func TestRenderStreamHEADWritesNoBody(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error { return c.RenderStream(http.StatusOK, comp("<h1>hi</h1>")) })

	if n := do(r, http.MethodHead, "/").Body.Len(); n != 0 {
		t.Errorf("body length = %d, want 0", n)
	}
}

func TestJSONPretty(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.JSONPretty(http.StatusAccepted, payload{Name: "Ada", Age: 37}, "\t")
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if got := rec.Header().Get(HeaderContentType); got != MIMEApplicationJSONCharsetUTF8 {
		t.Errorf("Content-Type = %q, want %q", got, MIMEApplicationJSONCharsetUTF8)
	}
	want := "{\n\t\"name\": \"Ada\",\n\t\"age\": 37\n}"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

type readCounter struct{ reads int }

func (r *readCounter) Read([]byte) (int, error) {
	r.reads++
	return 0, io.EOF
}

func TestStream(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.Stream(http.StatusAccepted, "application/x-lines", strings.NewReader("one\ntwo\n"))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if got := rec.Header().Get(HeaderContentType); got != "application/x-lines" {
		t.Errorf("Content-Type = %q, want %q", got, "application/x-lines")
	}
	if got := rec.Body.String(); got != "one\ntwo\n" {
		t.Errorf("body = %q, want %q", got, "one\ntwo\n")
	}
}

func TestStreamHEADDoesNotReadTheSource(t *testing.T) {
	source := new(readCounter)
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.Stream(http.StatusOK, MIMEOctetStream, source)
	})

	rec := do(r, http.MethodHead, "/")
	if source.reads != 0 {
		t.Errorf("source was read %d times, want 0", source.reads)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if got := rec.Header().Get(HeaderContentType); got != MIMEOctetStream {
		t.Errorf("Content-Type = %q, want %q", got, MIMEOctetStream)
	}
}

func TestRedirectAcceptsOnlyDefinedRedirectStatuses(t *testing.T) {
	valid := map[int]bool{
		http.StatusMultipleChoices:   true,
		http.StatusMovedPermanently:  true,
		http.StatusFound:             true,
		http.StatusSeeOther:          true,
		http.StatusTemporaryRedirect: true,
		http.StatusPermanentRedirect: true,
	}

	for status := 299; status <= 309; status++ {
		t.Run(http.StatusText(status), func(t *testing.T) {
			rec := httptest.NewRecorder()
			b := NewBase(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			err := b.Redirect(status, "/next")

			if valid[status] {
				if err != nil {
					t.Fatalf("Redirect(%d) = %v", status, err)
				}
				if rec.Code != status {
					t.Errorf("status = %d, want %d", rec.Code, status)
				}
				if got := rec.Header().Get(HeaderLocation); got != "/next" {
					t.Errorf("Location = %q, want %q", got, "/next")
				}
				return
			}

			if !errors.Is(err, ErrInternalServerError) {
				t.Errorf("Redirect(%d) = %v, want an internal error", status, err)
			}
			if b.Response().Committed {
				t.Error("an invalid redirect committed the response")
			}
			if got := rec.Header().Get(HeaderLocation); got != "" {
				t.Errorf("Location = %q, want empty", got)
			}
		})
	}
}

func TestKeepBuf(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  int
		want bool
	}{
		{"a fresh buffer", renderBufSize, true},
		{"exactly the ceiling", maxPooledRenderBuf, true},
		{"one byte over", maxPooledRenderBuf + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := bytes.NewBuffer(make([]byte, 0, tc.cap))
			if got := keepBuf(buf); got != tc.want {
				t.Errorf("keepBuf(cap %d) = %v, want %v", tc.cap, got, tc.want)
			}
		})
	}
}

func TestRenderKeepsAnHTTPErrorStatus(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.Render(http.StatusOK, ComponentFunc(func(context.Context, io.Writer) error {
			return ErrNotFound.WithMessage("no such post")
		}))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Body.String(); !strings.Contains(got, "no such post") {
		t.Errorf("body = %q, want the message of the component", got)
	}
}

func TestRenderHidesAnInternalError(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.Render(http.StatusOK, ComponentFunc(func(context.Context, io.Writer) error {
			return errors.New("connection string: user=root password=hunter2")
		}))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("body leaked the internal cause: %q", rec.Body.String())
	}
}

func TestRenderPanicKeepsThePoolUsable(t *testing.T) {
	r := newTestRouter()
	r.GET("/boom", func(c *tctx) error {
		return c.Render(http.StatusOK, ComponentFunc(func(_ context.Context, w io.Writer) error {
			if _, err := io.WriteString(w, "<html>partial"); err != nil {
				return err
			}
			panic("template blew up")
		}))
	})
	r.GET("/ok", func(c *tctx) error { return c.Render(http.StatusOK, comp("<h1>hi</h1>")) })

	if code := do(r, http.MethodGet, "/boom").Code; code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
	if got := do(r, http.MethodGet, "/ok").Body.String(); got != "<h1>hi</h1>" {
		t.Errorf("body = %q, want %q", got, "<h1>hi</h1>")
	}
}

func TestFromContextWithoutABase(t *testing.T) {
	b, ok := FromContext(context.Background())
	if ok {
		t.Errorf("ok = true, want false")
	}
	if b != nil {
		t.Errorf("base = %v, want nil", b)
	}
}

func BenchmarkRender(b *testing.B) {
	page := comp("<html><body><h1>hello</h1></body></html>")
	r := New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
	r.GET("/", func(c *tctx) error { return c.Render(http.StatusOK, page) })
	benchServe(b, r, &nopWriter{h: make(http.Header)}, "/")
}

func BenchmarkRenderStream(b *testing.B) {
	page := comp("<html><body><h1>hello</h1></body></html>")
	r := New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
	r.GET("/", func(c *tctx) error { return c.RenderStream(http.StatusOK, page) })
	benchServe(b, r, &nopWriter{h: make(http.Header)}, "/")
}
