package middleware_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

const floor = 500 * time.Millisecond

// stampedRecorder records when the header reached it. Under synctest that is
// the exact moment the header left the Response.
type stampedRecorder struct {
	*httptest.ResponseRecorder
	at      time.Time
	stamped bool
}

func (s *stampedRecorder) stamp() {
	if !s.stamped {
		s.at, s.stamped = time.Now(), true
	}
}

func (s *stampedRecorder) WriteHeader(code int) {
	s.stamp()
	s.ResponseRecorder.WriteHeader(code)
}

func (s *stampedRecorder) Write(b []byte) (int, error) {
	s.stamp()
	return s.ResponseRecorder.Write(b)
}

// serveTimed serves req and reports the answer with the time its header took
// to leave. For an answer the handler never wrote, that is when ServeHTTP
// returned, because net/http sends the empty 200 then.
func serveTimed(t *testing.T, h http.Handler, req *http.Request) (*stampedRecorder, time.Duration) {
	t.Helper()
	rec := &stampedRecorder{ResponseRecorder: httptest.NewRecorder()}
	start := time.Now()
	h.ServeHTTP(rec, req)
	rec.stamp()
	return rec, rec.at.Sub(start)
}

func getTimed(t *testing.T, h http.Handler, target string) (*stampedRecorder, time.Duration) {
	t.Helper()
	return serveTimed(t, h, httptest.NewRequest(http.MethodGet, target, nil))
}

func quietRouter() *router.Router[*appContext] {
	r := newRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	return r
}

func TestMinDurationHoldsEveryOutcome(t *testing.T) {
	tests := []struct {
		handler func(c *appContext) error
		name    string
		status  int
	}{
		{
			name:    "a written answer",
			handler: func(c *appContext) error { return c.String(http.StatusOK, "ok") },
			status:  http.StatusOK,
		},
		{
			name:    "an error the error handler writes",
			handler: func(*appContext) error { return router.ErrUnauthorized },
			status:  http.StatusUnauthorized,
		},
		{
			name:    "a panic",
			handler: func(*appContext) error { panic("boom") },
			status:  http.StatusInternalServerError,
		},
		{
			name:    "nothing written",
			handler: func(*appContext) error { return nil },
			status:  http.StatusOK,
		},
		{
			name: "a stream",
			handler: func(c *appContext) error {
				c.Response().Flush()
				return c.String(http.StatusOK, "data")
			},
			status: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := quietRouter()
				r.With(middleware.MinDuration[*appContext](floor)).GET("/login", tt.handler)

				rec, took := getTimed(t, r, "/login")
				if rec.Code != tt.status {
					t.Errorf("status = %d, want %d", rec.Code, tt.status)
				}
				if took != floor {
					t.Errorf("the header left after %v, want %v", took, floor)
				}
			})
		})
	}
}

func TestMinDurationAddsNothingToASlowAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := quietRouter()
		r.With(middleware.MinDuration[*appContext](floor)).GET("/login", func(c *appContext) error {
			time.Sleep(2 * floor)
			return c.String(http.StatusOK, "ok")
		})

		if _, took := getTimed(t, r, "/login"); took != 2*floor {
			t.Errorf("the header left after %v, want %v", took, 2*floor)
		}
	})
}

func TestMinDurationCountsFromTheMiddleware(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const outer = 300 * time.Millisecond
		r := quietRouter()
		r.Use(func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
			return func(c *appContext) error {
				time.Sleep(outer)
				return next(c)
			}
		})
		r.With(middleware.MinDuration[*appContext](floor)).GET("/login", func(c *appContext) error {
			return c.String(http.StatusOK, "ok")
		})

		if _, took := getTimed(t, r, "/login"); took != outer+floor {
			t.Errorf("the header left after %v, want %v", took, outer+floor)
		}
	})
}

func TestMinDurationLetsTheAnswerGoWhenTheClientLeaves(t *testing.T) {
	newFloored := func() *router.Router[*appContext] {
		r := quietRouter()
		r.With(middleware.MinDuration[*appContext](floor)).GET("/login", func(c *appContext) error {
			return c.String(http.StatusOK, "ok")
		})
		return r
	}

	t.Run("while held", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			time.AfterFunc(100*time.Millisecond, cancel)

			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/login", nil)
			if _, took := serveTimed(t, newFloored(), req); took != 100*time.Millisecond {
				t.Errorf("the header left after %v, want 100ms", took)
			}
		})
	})

	t.Run("already gone", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/login", nil)
			if _, took := serveTimed(t, newFloored(), req); took != 0 {
				t.Errorf("the header left after %v, want at once", took)
			}
		})
	})
}

func TestMinDurationOutlastsAnInnerTimeout(t *testing.T) {
	const deadline = 100 * time.Millisecond
	timeout := middleware.TimeoutWithConfig(middleware.TimeoutConfig[*appContext]{Duration: deadline})
	floored := middleware.MinDuration[*appContext](floor)
	ok := func(c *appContext) error { return c.String(http.StatusOK, "ok") }

	t.Run("timeout inside", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := quietRouter()
			r.With(floored, timeout).GET("/login", ok)

			rec, took := getTimed(t, r, "/login")
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if took != floor {
				t.Errorf("the header left after %v, want %v", took, floor)
			}
		})
	})

	t.Run("timeout outside", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := quietRouter()
			r.With(timeout, floored).GET("/login", ok)

			if _, took := getTimed(t, r, "/login"); took != deadline {
				t.Errorf("the header left after %v, want %v", took, deadline)
			}
		})
	})
}

func TestMinDurationKeepsTheHeadersOfOtherHooks(t *testing.T) {
	keyAuth := middleware.KeyAuth(func(_ *appContext, key string) (bool, error) {
		return key == "s3cret", nil
	})
	floored := middleware.MinDuration[*appContext](floor)
	ok := func(c *appContext) error { return c.String(http.StatusOK, "ok") }

	t.Run("a refusal in front is not held", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := quietRouter()
			r.With(keyAuth, floored).GET("/login", ok)

			rec, took := getTimed(t, r, "/login")
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if took != 0 {
				t.Errorf("the header left after %v, want at once", took)
			}
		})
	})

	t.Run("a refusal behind is held", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := quietRouter()
			r.With(floored, keyAuth).GET("/login", ok)

			rec, took := getTimed(t, r, "/login")
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if rec.Header().Get(router.HeaderWWWAuthenticate) == "" {
				t.Error("the 401 lost its WWW-Authenticate header")
			}
			if took != floor {
				t.Errorf("the header left after %v, want %v", took, floor)
			}
		})
	})

	t.Run("a writer of the handler", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := quietRouter()
			r.With(floored).GET("/login", func(c *appContext) error {
				res := c.Response()
				res.ResponseWriter = &lateHeaderWriter{ResponseWriter: res.ResponseWriter}
				return c.String(http.StatusOK, "ok")
			})

			rec, took := getTimed(t, r, "/login")
			if got := rec.Header().Get("X-Late"); got != "yes" {
				t.Errorf("X-Late = %q, want yes", got)
			}
			if took != floor {
				t.Errorf("the header left after %v, want %v", took, floor)
			}
		})
	})
}

// lateHeaderWriter sets a header at the last moment, as the header goes out.
type lateHeaderWriter struct {
	http.ResponseWriter
}

func (w *lateHeaderWriter) WriteHeader(code int) {
	w.Header().Set("X-Late", "yes")
	w.ResponseWriter.WriteHeader(code)
}

func TestMinDurationStacksWithGzip(t *testing.T) {
	floored := middleware.MinDuration[*appContext](floor)
	long := func(c *appContext) error { return c.HTML(http.StatusOK, gzipLongBody) }
	tests := []struct {
		name  string
		chain []router.Middleware[*appContext]
	}{
		{"gzip in front", []router.Middleware[*appContext]{middleware.Gzip[*appContext], floored}},
		{"gzip behind", []router.Middleware[*appContext]{floored, middleware.Gzip[*appContext]}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := quietRouter()
				r.With(tt.chain...).GET("/login", long)

				req := httptest.NewRequest(http.MethodGet, "/login", nil)
				req.Header.Set(router.HeaderAcceptEncoding, "gzip")
				rec, took := serveTimed(t, r, req)
				if got := rec.Header().Get(router.HeaderContentEncoding); got != "gzip" {
					t.Fatalf("Content-Encoding = %q, want gzip", got)
				}
				if got := ungzip(t, rec.ResponseRecorder); got != gzipLongBody {
					t.Errorf("body = %q, want the long body", got)
				}
				if took != floor {
					t.Errorf("the header left after %v, want %v", took, floor)
				}
			})
		})
	}
}

func TestMinDurationStacksWithCapture(t *testing.T) {
	var got router.Recorded
	capture := func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			stop := c.Response().Capture(1 << 10)
			err := next(c)
			got = stop()
			return err
		}
	}
	floored := middleware.MinDuration[*appContext](floor)
	tests := []struct {
		name  string
		chain []router.Middleware[*appContext]
	}{
		{"capture in front", []router.Middleware[*appContext]{capture, floored}},
		{"capture behind", []router.Middleware[*appContext]{floored, capture}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				got = router.Recorded{}
				r := quietRouter()
				r.With(tt.chain...).GET("/login", func(c *appContext) error {
					return c.String(http.StatusCreated, "sent")
				})

				rec, took := getTimed(t, r, "/login")
				if rec.Code != http.StatusCreated || rec.Body.String() != "sent" {
					t.Errorf("answer = %d %q, want 201 sent", rec.Code, rec.Body.String())
				}
				if got.Status != http.StatusCreated || string(got.Body) != "sent" {
					t.Errorf("recorded = %d %q, want 201 sent", got.Status, got.Body)
				}
				if took != floor {
					t.Errorf("the header left after %v, want %v", took, floor)
				}
			})
		})
	}
}

func TestMinDurationSkip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := quietRouter()
		r.Route("/login", func(g *router.Router[*appContext]) {
			g.Use(middleware.MinDurationWithConfig(middleware.MinDurationConfig[*appContext]{
				Duration: floor,
				Skip:     func(c *appContext) bool { return c.Request().Method == http.MethodGet },
			}))
			g.GET("/", func(c *appContext) error { return c.String(http.StatusOK, "form") })
			g.POST("/", func(c *appContext) error { return c.String(http.StatusOK, "sent") })
		})

		if _, took := getTimed(t, r, "/login"); took != 0 {
			t.Errorf("GET: the header left after %v, want at once", took)
		}
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		if _, took := serveTimed(t, r, req); took != floor {
			t.Errorf("POST: the header left after %v, want %v", took, floor)
		}
	})
}

func TestMinDurationNeedsADuration(t *testing.T) {
	mustPanicContaining(t, "needs a Duration above zero", func() {
		middleware.MinDuration[*appContext](0)
	})
	mustPanicContaining(t, "needs a Duration above zero", func() {
		middleware.MinDuration[*appContext](-time.Second)
	})
	mustPanicContaining(t, "needs a Duration above zero", func() {
		middleware.MinDurationWithConfig(middleware.MinDurationConfig[*appContext]{})
	})
}

func TestMinDurationStaysWithItsRequestOnAPooledRouter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := router.NewPooled(func() *appContext { return new(appContext) }, func(*appContext) {})
		r.Logger(slog.New(slog.DiscardHandler))
		r.With(middleware.MinDuration[*appContext](floor)).POST("/login", func(c *appContext) error {
			return c.String(http.StatusOK, "sent")
		})
		r.GET("/other", func(c *appContext) error { return c.String(http.StatusOK, "other") })

		for i := range 50 {
			req := httptest.NewRequest(http.MethodPost, "/login", nil)
			if _, took := serveTimed(t, r, req); took != floor {
				t.Fatalf("round %d: POST left after %v, want %v", i, took, floor)
			}
			if _, took := getTimed(t, r, "/other"); took != 0 {
				t.Fatalf("round %d: GET left after %v, want at once", i, took)
			}
		}
	})
}
