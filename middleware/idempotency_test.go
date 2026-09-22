package middleware_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// fakeIdempotencyStore wraps the memory store to count the calls, note when
// each Claim ran, and fail on demand.
type fakeIdempotencyStore struct {
	middleware.IdempotencyStore[*appContext]
	claimErr    error
	completeErr error
	claimTimes  []time.Time
	mu          sync.Mutex
	claims      atomic.Int32
	completes   atomic.Int32
	releases    atomic.Int32
}

func newFakeIdempotencyStore() *fakeIdempotencyStore {
	return &fakeIdempotencyStore{IdempotencyStore: middleware.NewIdempotencyMemoryStore[*appContext](0)}
}

func (s *fakeIdempotencyStore) Claim(c *appContext, key string, fp []byte) (middleware.IdempotencyEntry, bool, error) {
	s.claims.Add(1)
	s.mu.Lock()
	s.claimTimes = append(s.claimTimes, time.Now())
	s.mu.Unlock()
	if s.claimErr != nil {
		return middleware.IdempotencyEntry{}, false, s.claimErr
	}
	return s.IdempotencyStore.Claim(c, key, fp)
}

func (s *fakeIdempotencyStore) Complete(c *appContext, key string, rec router.Recorded) error {
	s.completes.Add(1)
	if s.completeErr != nil {
		return s.completeErr
	}
	return s.IdempotencyStore.Complete(c, key, rec)
}

func (s *fakeIdempotencyStore) Release(c *appContext, key string) error {
	s.releases.Add(1)
	return s.IdempotencyStore.Release(c, key)
}

func (s *fakeIdempotencyStore) lastClaim() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimTimes[len(s.claimTimes)-1]
}

// seenErrors is an error handler that keeps every error it answers.
type seenErrors struct {
	errs []error
	mu   sync.Mutex
}

func (s *seenErrors) handle(c *appContext, err error) error {
	s.mu.Lock()
	s.errs = append(s.errs, err)
	s.mu.Unlock()
	return router.DefaultErrorHandler(c, err)
}

func (s *seenErrors) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.errs)
}

func (s *seenErrors) last() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errs) == 0 {
		return nil
	}
	return s.errs[len(s.errs)-1]
}

func idempotencyRouter(seen *seenErrors, mws ...router.Middleware[*appContext]) *router.Router[*appContext] {
	r := newRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	if seen != nil {
		r.ErrorHandler(seen.handle)
	}
	r.Use(mws...)
	return r
}

func idempotentRequest(method, target, key string, form url.Values) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationForm)
	if key != "" {
		req.Header.Set(router.HeaderIdempotencyKey, key)
	}
	return req
}

func postKey(h http.Handler, target, key string, form url.Values) *httptest.ResponseRecorder {
	return do(h, idempotentRequest(http.MethodPost, target, key, form))
}

// payments charges once per run and says which run it was.
type payments struct {
	gate    chan struct{}
	entered chan struct{}
	runs    atomic.Int32
}

func (p *payments) pay(c *appContext) error {
	n := p.runs.Add(1)
	if p.entered != nil {
		p.entered <- struct{}{}
	}
	if p.gate != nil {
		<-p.gate
	}
	c.Response().Header().Set(router.HeaderHXRedirect, "/receipts/"+strconv.Itoa(int(n)))
	http.SetCookie(c.Response(), &http.Cookie{Name: "_flash", Value: "paid"})
	return c.Stringf(http.StatusCreated, "payment %d of %s", n, c.FormValue("amount"))
}

func amount(v string) url.Values { return url.Values{"amount": {v}} }

func sameAnswer(t *testing.T, a, b *httptest.ResponseRecorder) {
	t.Helper()
	if a.Code != b.Code || a.Body.String() != b.Body.String() {
		t.Errorf("answers differ: %d %q and %d %q", a.Code, a.Body, b.Code, b.Body)
	}
	for _, k := range []string{router.HeaderHXRedirect, "Set-Cookie"} {
		if got, want := b.Header().Values(k), a.Header().Values(k); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// Regression: a double submit of a payment form charged twice.
func TestIdempotencyRunsADoubleSubmitOnce(t *testing.T) {
	p := &payments{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	store := newFakeIdempotencyStore()
	r := idempotencyRouter(nil, middleware.Idempotency(store))
	r.POST("/pay", p.pay)

	var first, second *httptest.ResponseRecorder
	var wg sync.WaitGroup
	wg.Go(func() { first = postKey(r, "/pay", "k1", amount("5")) })
	<-p.entered
	wg.Go(func() { second = postKey(r, "/pay", "k1", amount("5")) })
	for store.claims.Load() < 3 {
		time.Sleep(time.Millisecond)
	}
	close(p.gate)
	wg.Wait()

	if first.Code != http.StatusCreated || first.Body.String() != "payment 1 of 5" {
		t.Fatalf("first = %d %q", first.Code, first.Body)
	}
	sameAnswer(t, first, second)
	sameAnswer(t, first, postKey(r, "/pay", "k1", amount("5")))
	if n := p.runs.Load(); n != 1 {
		t.Errorf("the handler ran %d times, want 1", n)
	}
}

func TestIdempotencyRefusesAReusedKeyWithAnotherRequest(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		form   url.Values
	}{
		{name: "another amount", method: http.MethodPost, target: "/pay", form: amount("9")},
		{name: "another path", method: http.MethodPost, target: "/refund", form: amount("5")},
		{name: "another method", method: http.MethodPut, target: "/pay", form: amount("5")},
	}
	for _, running := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(tt.name+" running="+strconv.FormatBool(running), func(t *testing.T) {
				p := &payments{}
				if running {
					p.gate, p.entered = make(chan struct{}), make(chan struct{}, 1)
				}
				seen := &seenErrors{}
				r := idempotencyRouter(seen, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
				r.POST("/pay", p.pay)
				r.POST("/refund", p.pay)
				r.PUT("/pay", p.pay)

				var first *httptest.ResponseRecorder
				var wg sync.WaitGroup
				wg.Go(func() { first = postKey(r, "/pay", "k", amount("5")) })
				if running {
					<-p.entered
				} else {
					wg.Wait()
				}

				rec := do(r, idempotentRequest(tt.method, tt.target, "k", tt.form))
				if rec.Code != http.StatusUnprocessableEntity {
					t.Errorf("status = %d, want 422", rec.Code)
				}
				if !errors.Is(seen.last(), middleware.ErrIdempotencyKeyReused) {
					t.Errorf("error = %v, want ErrIdempotencyKeyReused", seen.last())
				}
				if running {
					close(p.gate)
				}
				wg.Wait()

				sameAnswer(t, first, postKey(r, "/pay", "k", amount("5")))
				if n := p.runs.Load(); n != 1 {
					t.Errorf("the handler ran %d times, want 1", n)
				}
			})
		}
	}
}

func TestIdempotencyRefusesAMismatchWithoutWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &payments{gate: make(chan struct{})}
		store := newFakeIdempotencyStore()
		r := idempotencyRouter(nil, middleware.Idempotency(store))
		r.POST("/pay", p.pay)

		var wg sync.WaitGroup
		wg.Go(func() { postKey(r, "/pay", "k", amount("5")) })
		synctest.Wait()

		start := time.Now()
		rec := postKey(r, "/pay", "k", amount("9"))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422", rec.Code)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("the mismatch waited %v, want no wait", elapsed)
		}
		if n := store.claims.Load(); n != 2 {
			t.Errorf("Claim ran %d times, want 2", n)
		}
		close(p.gate)
		wg.Wait()
	})
}

func TestIdempotencyRequiredRefusesAMissingKey(t *testing.T) {
	p := &payments{}
	store := newFakeIdempotencyStore()
	seen := &seenErrors{}
	r := idempotencyRouter(seen, middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*appContext]{
		Store: store, Required: true,
	}))
	r.POST("/pay", p.pay)
	r.GET("/pay", p.pay)

	if rec := postKey(r, "/pay", "", amount("5")); rec.Code != http.StatusBadRequest {
		t.Errorf("POST without a key: status = %d, want 400", rec.Code)
	}
	if !errors.Is(seen.last(), middleware.ErrIdempotencyKeyRequired) {
		t.Errorf("error = %v, want ErrIdempotencyKeyRequired", seen.last())
	}
	if n := store.claims.Load(); n != 0 {
		t.Errorf("Claim ran %d times, want 0", n)
	}
	if rec := get(r, "/pay"); rec.Code != http.StatusCreated {
		t.Errorf("GET without a key: status = %d, want 201", rec.Code)
	}

	optional := idempotencyRouter(nil, middleware.Idempotency(store))
	optional.POST("/pay", p.pay)
	before := p.runs.Load()
	postKey(optional, "/pay", "", amount("5"))
	postKey(optional, "/pay", "", amount("5"))
	if n := p.runs.Load() - before; n != 2 {
		t.Errorf("two posts without a key ran %d times, want 2", n)
	}
}

func TestIdempotencyAnswersAConflictWhileTheFirstRuns(t *testing.T) {
	t.Run("default wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &payments{gate: make(chan struct{})}
			store := newFakeIdempotencyStore()
			seen := &seenErrors{}
			r := idempotencyRouter(seen, middleware.Idempotency(store))
			r.POST("/pay", p.pay)

			var wg sync.WaitGroup
			wg.Go(func() { postKey(r, "/pay", "k", amount("5")) })
			synctest.Wait()

			start := time.Now()
			rec := postKey(r, "/pay", "k", amount("5"))
			if rec.Code != http.StatusConflict || !errors.Is(seen.last(), middleware.ErrIdempotencyInProgress) {
				t.Errorf("status = %d, error = %v; want 409 and ErrIdempotencyInProgress", rec.Code, seen.last())
			}
			if elapsed := time.Since(start); elapsed != middleware.DefaultIdempotencyWait {
				t.Errorf("the repeat waited %v, want %v", elapsed, middleware.DefaultIdempotencyWait)
			}
			if last := store.lastClaim(); !last.Equal(start.Add(middleware.DefaultIdempotencyWait)) {
				t.Errorf("the last Claim ran %v after the start, want it on the deadline", last.Sub(start))
			}
			close(p.gate)
			wg.Wait()
			if n := p.runs.Load(); n != 1 {
				t.Errorf("the handler ran %d times, want 1", n)
			}
		})
	})

	t.Run("negative wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &payments{gate: make(chan struct{})}
			store := newFakeIdempotencyStore()
			r := idempotencyRouter(nil, middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*appContext]{
				Store: store, Wait: -1,
			}))
			r.POST("/pay", p.pay)

			var wg sync.WaitGroup
			wg.Go(func() { postKey(r, "/pay", "k", amount("5")) })
			synctest.Wait()

			start := time.Now()
			if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusConflict {
				t.Errorf("status = %d, want 409", rec.Code)
			}
			if elapsed := time.Since(start); elapsed != 0 || store.claims.Load() != 2 {
				t.Errorf("waited %v after %d claims, want no wait and 2", elapsed, store.claims.Load())
			}
			close(p.gate)
			wg.Wait()
			if n := p.runs.Load(); n != 1 {
				t.Errorf("the handler ran %d times, want 1", n)
			}
		})
	})

	t.Run("released inside the wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var runs atomic.Int32
			r := idempotencyRouter(nil, middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*appContext]{
				Store: middleware.NewIdempotencyMemoryStore[*appContext](0), Wait: 50 * time.Millisecond,
			}))
			r.POST("/pay", func(c *appContext) error {
				if runs.Add(1) == 1 {
					time.Sleep(30 * time.Millisecond)
					return router.ErrServiceUnavailable
				}
				return c.String(http.StatusCreated, "paid")
			})

			var wg sync.WaitGroup
			wg.Go(func() { postKey(r, "/pay", "k", amount("5")) })
			synctest.Wait()

			if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusCreated {
				t.Errorf("status = %d, want 201 once the key came free", rec.Code)
			}
			wg.Wait()
		})
	})
}

func TestIdempotencyTakesAKeyReleasedWhileWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		var runs atomic.Int32
		r := idempotencyRouter(nil, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
		r.POST("/pay", func(c *appContext) error {
			if runs.Add(1) == 1 {
				<-gate
				return c.String(http.StatusBadGateway, "the bank is down")
			}
			return c.String(http.StatusCreated, "paid")
		})

		var wg sync.WaitGroup
		wg.Go(func() { postKey(r, "/pay", "k", amount("5")) })
		synctest.Wait()

		var repeat *httptest.ResponseRecorder
		wg.Go(func() { repeat = postKey(r, "/pay", "k", amount("5")) })
		time.Sleep(time.Second)
		close(gate)
		wg.Wait()

		if repeat.Code != http.StatusCreated || runs.Load() != 2 {
			t.Errorf("repeat = %d after %d runs, want 201 after 2", repeat.Code, runs.Load())
		}
	})
}

func TestIdempotencyStopsWaitingWhenTheClientLeaves(t *testing.T) {
	p := &payments{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	r := newRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	var got error
	r.Use(func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			err := next(c)
			if c.Request().Header.Get("X-Leaves") != "" {
				got = err
			}
			return err
		}
	}, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", p.pay)

	var wg sync.WaitGroup
	wg.Go(func() { postKey(r, "/pay", "k", amount("5")) })
	<-p.entered

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := idempotentRequest(http.MethodPost, "/pay", "k", amount("5")).WithContext(ctx)
	req.Header.Set("X-Leaves", "1")
	start := time.Now()
	do(r, req)
	if !errors.Is(got, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the repeat waited %v after its client left", elapsed)
	}
	close(p.gate)
	wg.Wait()
}

func TestIdempotencyScopesKeys(t *testing.T) {
	p := &payments{}
	store := newFakeIdempotencyStore()
	seen := &seenErrors{}
	r := idempotencyRouter(seen, middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*appContext]{
		Store: store,
		Scope: func(c *appContext) (string, error) {
			user := c.Request().Header.Get("X-User")
			if user == "anonymous" {
				return "", errors.New("no session")
			}
			return user, nil
		},
	}))
	r.POST("/pay", p.pay)

	as := func(user string) *httptest.ResponseRecorder {
		req := idempotentRequest(http.MethodPost, "/pay", "k", amount("5"))
		req.Header.Set("X-User", user)
		return do(r, req)
	}
	alice := as("alice")
	bob := as("bob")
	if alice.Body.String() == bob.Body.String() {
		t.Errorf("two users shared a key: %q", bob.Body)
	}
	sameAnswer(t, alice, as("alice"))

	claims := store.claims.Load()
	if rec := as("anonymous"); rec.Code != http.StatusForbidden {
		t.Errorf("a Scope error: status = %d, want 403", rec.Code)
	}
	if store.claims.Load() != claims {
		t.Error("a Scope error reached the store")
	}

	nobody := as("")
	sameAnswer(t, nobody, as(""))
	if n := p.runs.Load(); n != 3 {
		t.Errorf("the handler ran %d times, want 3", n)
	}
}

func TestIdempotencyReleasesAKeyWhenTheHandlerFails(t *testing.T) {
	var runs atomic.Int32
	store := newFakeIdempotencyStore()
	r := idempotencyRouter(nil, middleware.Idempotency(store))
	r.POST("/pay", func(c *appContext) error {
		if runs.Add(1) == 1 {
			return router.ErrBadRequest.WithMessage("the card was declined")
		}
		return c.String(http.StatusCreated, "paid")
	})

	if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusBadRequest {
		t.Fatalf("first = %d, want 400", rec.Code)
	}
	if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusCreated {
		t.Errorf("retry = %d, want 201", rec.Code)
	}
	if store.releases.Load() != 1 || runs.Load() != 2 {
		t.Errorf("releases = %d, runs = %d; want 1 and 2", store.releases.Load(), runs.Load())
	}
}

func TestIdempotencyRunsARepeatOfA5xxAgain(t *testing.T) {
	var runs atomic.Int32
	r := idempotencyRouter(nil, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		if runs.Add(1) == 1 {
			return c.String(http.StatusBadGateway, "the bank is down")
		}
		return c.String(http.StatusCreated, "paid")
	})

	postKey(r, "/pay", "k", amount("5"))
	if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusCreated || runs.Load() != 2 {
		t.Errorf("retry = %d after %d runs, want 201 after 2", rec.Code, runs.Load())
	}
}

func TestIdempotencyReplaysARedirectWithNoBody(t *testing.T) {
	var runs atomic.Int32
	r := idempotencyRouter(nil, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		runs.Add(1)
		return c.Redirect(http.StatusSeeOther, "/receipt")
	})

	postKey(r, "/pay", "k", amount("5"))
	rec := postKey(r, "/pay", "k", amount("5"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get(router.HeaderLocation) != "/receipt" || rec.Body.Len() != 0 {
		t.Errorf("replay = %d %q %q, want 303 to /receipt with no body",
			rec.Code, rec.Header().Get(router.HeaderLocation), rec.Body)
	}
	if runs.Load() != 1 {
		t.Errorf("the handler ran %d times, want 1", runs.Load())
	}
}

func TestIdempotencyReleasesAKeyOnATimeout(t *testing.T) {
	for _, status := range []int{0, http.StatusRequestTimeout} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var runs atomic.Int32
			store := newFakeIdempotencyStore()
			r := idempotencyRouter(nil,
				middleware.Idempotency(store),
				middleware.TimeoutWithConfig[*appContext](middleware.TimeoutConfig{
					Duration: 10 * time.Millisecond,
					Status:   status,
				}))
			r.POST("/pay", func(c *appContext) error {
				if runs.Add(1) == 1 {
					<-c.Done()
					return c.Err()
				}
				return c.String(http.StatusCreated, "paid")
			})

			want := http.StatusServiceUnavailable
			if status != 0 {
				want = status
			}
			if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != want {
				t.Fatalf("first = %d, want %d", rec.Code, want)
			}
			if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusCreated {
				t.Errorf("retry = %d, want 201", rec.Code)
			}
			if store.releases.Load() != 1 {
				t.Errorf("releases = %d, want 1", store.releases.Load())
			}
		})
	}
}

// Regression: an answer too large to keep released the key, so the repeat
// charged again.
func TestIdempotencyKeepsTheKeyOfAnAnswerTooLargeToKeep(t *testing.T) {
	var runs atomic.Int32
	seen := &seenErrors{}
	r := idempotencyRouter(seen, middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*appContext]{
		Store: middleware.NewIdempotencyMemoryStore[*appContext](0), MaxBody: 4,
	}))
	r.POST("/pay", func(c *appContext) error {
		runs.Add(1)
		return c.String(http.StatusCreated, "a receipt too long to keep")
	})

	if rec := postKey(r, "/pay", "k", amount("5")); rec.Body.String() != "a receipt too long to keep" {
		t.Fatalf("first = %q, want the whole answer", rec.Body)
	}
	rec := postKey(r, "/pay", "k", amount("5"))
	if rec.Code != http.StatusConflict || !errors.Is(seen.last(), middleware.ErrIdempotencyTooLarge) {
		t.Errorf("repeat = %d, error = %v; want 409 and ErrIdempotencyTooLarge", rec.Code, seen.last())
	}
	if runs.Load() != 1 {
		t.Errorf("the handler ran %d times, want 1", runs.Load())
	}
}

// Regression: a store that failed to keep the answer released the key, so the
// repeat charged again.
func TestIdempotencyKeepsTheKeyWhenCompleteFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int32
		var logs bytes.Buffer
		store := newFakeIdempotencyStore()
		store.completeErr = errors.New("redis is down")
		r := idempotencyRouter(nil, middleware.Idempotency(store))
		r.Logger(slog.New(slog.NewTextHandler(&logs, nil)))
		r.POST("/pay", func(c *appContext) error {
			runs.Add(1)
			return c.String(http.StatusCreated, "paid")
		})

		if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusCreated {
			t.Fatalf("first = %d, want 201", rec.Code)
		}
		if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "redis is down") {
			t.Errorf("log = %q, want a warning with the cause", logs.String())
		}

		start := time.Now()
		if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusConflict {
			t.Errorf("repeat = %d, want 409", rec.Code)
		}
		if elapsed := time.Since(start); elapsed != middleware.DefaultIdempotencyWait {
			t.Errorf("the repeat waited %v, want %v", elapsed, middleware.DefaultIdempotencyWait)
		}
		if runs.Load() != 1 || store.releases.Load() != 0 {
			t.Errorf("runs = %d, releases = %d; want 1 and 0", runs.Load(), store.releases.Load())
		}
	})
}

type brokenWriter struct {
	*httptest.ResponseRecorder
}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("the client went away") }

func (brokenWriter) WriteString(string) (int, error) { return 0, errors.New("the client went away") }

func TestIdempotencyKeepsTheKeyOfASuccessWhoseWriteFailed(t *testing.T) {
	var runs atomic.Int32
	r := idempotencyRouter(nil, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		runs.Add(1)
		return c.String(http.StatusCreated, "paid")
	})

	r.ServeHTTP(brokenWriter{httptest.NewRecorder()}, idempotentRequest(http.MethodPost, "/pay", "k", amount("5")))
	rec := postKey(r, "/pay", "k", amount("5"))
	if rec.Code != http.StatusCreated || rec.Body.String() != "paid" || runs.Load() != 1 {
		t.Errorf("repeat = %d %q after %d runs, want the stored 201 after 1", rec.Code, rec.Body, runs.Load())
	}
}

func TestIdempotencyReleasesTheKeyOfAPanicAndKeepsTheStack(t *testing.T) {
	var runs atomic.Int32
	seen := &seenErrors{}
	r := idempotencyRouter(seen,
		middleware.LoggerWithConfig[*appContext](middleware.LoggerConfig{Logger: slog.New(slog.DiscardHandler)}),
		middleware.Recover[*appContext],
		middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		if runs.Add(1) == 1 {
			panic("the ledger is locked")
		}
		return c.String(http.StatusCreated, "paid")
	})

	if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusInternalServerError {
		t.Fatalf("first = %d, want 500", rec.Code)
	}
	pv, ok := errors.AsType[*router.PanicValue](seen.last())
	if !ok || !strings.Contains(string(pv.Stack), "TestIdempotencyReleasesTheKeyOfAPanicAndKeepsTheStack") {
		t.Errorf("error = %v, want a PanicValue whose stack names the handler", seen.last())
	}
	if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusCreated {
		t.Errorf("retry = %d, want 201", rec.Code)
	}
}

func TestIdempotencyHoldsTheKeyOfAPanicAfterASuccess(t *testing.T) {
	var runs atomic.Int32
	store := newFakeIdempotencyStore()
	r := idempotencyRouter(nil,
		middleware.Recover[*appContext],
		middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*appContext]{Store: store, Wait: -1}))
	r.POST("/pay", func(c *appContext) error {
		runs.Add(1)
		if err := c.String(http.StatusCreated, "paid"); err != nil {
			return err
		}
		panic("the audit log is down")
	})

	if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusCreated {
		t.Fatalf("first = %d, want the 201 that went out", rec.Code)
	}
	if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusConflict {
		t.Errorf("repeat = %d, want 409", rec.Code)
	}
	if store.releases.Load() != 0 || runs.Load() != 1 {
		t.Errorf("releases = %d, runs = %d; want 0 and 1", store.releases.Load(), runs.Load())
	}
}

func TestIdempotencyNeverCallsHandleError(t *testing.T) {
	p := &payments{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	seen := &seenErrors{}
	r := idempotencyRouter(seen, middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*appContext]{
		Store: middleware.NewIdempotencyMemoryStore[*appContext](0), Wait: -1, Required: true,
	}))
	r.POST("/pay", p.pay)

	var wg sync.WaitGroup
	wg.Go(func() { postKey(r, "/pay", "k", amount("5")) })
	<-p.entered

	for _, tt := range []struct {
		form url.Values
		key  string
		want int
	}{
		{key: "k", form: amount("5"), want: http.StatusConflict},
		{key: "k", form: amount("9"), want: http.StatusUnprocessableEntity},
		{key: "", form: amount("5"), want: http.StatusBadRequest},
	} {
		before := seen.count()
		if rec := postKey(r, "/pay", tt.key, tt.form); rec.Code != tt.want {
			t.Errorf("status = %d, want %d", rec.Code, tt.want)
		}
		if n := seen.count() - before; n != 1 {
			t.Errorf("a %d reached the error handler %d times, want 1", tt.want, n)
		}
	}
	close(p.gate)
	wg.Wait()

	before := seen.count()
	postKey(r, "/pay", "k", amount("5"))
	postKey(r, "/pay", "k2", amount("5"))
	if n := seen.count() - before; n != 0 {
		t.Errorf("a replay and a stored success reached the error handler %d times, want 0", n)
	}
}

func TestIdempotencyKeepsFlushWorking(t *testing.T) {
	r := idempotencyRouter(nil, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		if _, err := c.Response().WriteString("part"); err != nil {
			return err
		}
		c.Response().Flush()
		return nil
	})

	first := postKey(r, "/pay", "k", amount("5"))
	if !first.Flushed || first.Body.String() != "part" {
		t.Errorf("first: flushed = %v, body = %q; want true, %q", first.Flushed, first.Body, "part")
	}
	if rec := postKey(r, "/pay", "k", amount("5")); rec.Code != http.StatusOK || rec.Body.String() != "part" {
		t.Errorf("replay = %d %q, want 200 %q", rec.Code, rec.Body, "part")
	}
}

func TestIdempotencyReplaysOnlyTheHeadersTheHandlerSet(t *testing.T) {
	r := idempotencyRouter(nil,
		middleware.RequestID[*appContext],
		middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		c.Response().Header().Set("X-Charge", "ch_1")
		return c.String(http.StatusCreated, "paid")
	})

	first := postKey(r, "/pay", "k", amount("5"))
	replay := postKey(r, "/pay", "k", amount("5"))
	ids := replay.Header().Values(router.HeaderXRequestID)
	if len(ids) != 1 || ids[0] == first.Header().Get(router.HeaderXRequestID) {
		t.Errorf("replay request ids = %q, want one of its own", ids)
	}
	if replay.Header().Get("X-Charge") != "ch_1" {
		t.Errorf("replay X-Charge = %q, want ch_1", replay.Header().Get("X-Charge"))
	}
}

func TestIdempotencyReplaysOnlyTheCookiesTheHandlerSet(t *testing.T) {
	r := idempotencyRouter(nil,
		middleware.CSRF[*appContext],
		middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		http.SetCookie(c.Response(), &http.Cookie{Name: "receipt", Value: "r1"})
		return c.String(http.StatusCreated, "paid")
	})

	send := func() *httptest.ResponseRecorder {
		req := idempotentRequest(http.MethodPost, "/pay", "k", amount("5"))
		req.Header.Set(router.HeaderSecFetchSite, "same-origin")
		return do(r, req)
	}
	send()
	replay := send()
	names := map[string]int{}
	for _, c := range replay.Result().Cookies() {
		names[c.Name]++
	}
	if names[middleware.DefaultCSRFCookieName] != 1 || names["receipt"] != 1 || len(names) != 2 {
		t.Errorf("replay cookies = %v, want one CSRF cookie and one receipt", names)
	}
}

// Regression: a replay sent the session cookie that a callback in front set on
// the first request, next to the one it set for the repeat.
func TestIdempotencyDoesNotReplayACookieAnOuterCallbackSet(t *testing.T) {
	var sessions atomic.Int32
	session := func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			sid := strconv.Itoa(int(sessions.Add(1)))
			c.Response().Before(func() {
				http.SetCookie(c.Response(), &http.Cookie{Name: "sid", Value: sid})
			})
			return next(c)
		}
	}
	r := idempotencyRouter(nil, session, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		http.SetCookie(c.Response(), &http.Cookie{Name: "receipt", Value: "r1"})
		return c.String(http.StatusCreated, "paid")
	})

	postKey(r, "/pay", "k", amount("5"))
	replay := postKey(r, "/pay", "k", amount("5"))
	got := map[string][]string{}
	for _, c := range replay.Result().Cookies() {
		got[c.Name] = append(got[c.Name], c.Value)
	}
	if strings.Join(got["sid"], "|") != "2" || strings.Join(got["receipt"], "|") != "r1" || len(got) != 2 {
		t.Errorf("replay cookies = %v, want sid 2 and receipt r1", got)
	}
}

// Regression: a replay kept a header the handler had removed from the answer.
func TestIdempotencyReplaysAHeaderTheHandlerRemoved(t *testing.T) {
	noStore := func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			c.Response().Header().Set(router.HeaderCacheControl, "no-store")
			return next(c)
		}
	}
	r := idempotencyRouter(nil, noStore, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		c.Response().Header().Del(router.HeaderCacheControl)
		return c.String(http.StatusCreated, "paid")
	})

	first := postKey(r, "/pay", "k", amount("5"))
	replay := postKey(r, "/pay", "k", amount("5"))
	if got, want := replay.Header().Values(router.HeaderCacheControl), first.Header().Values(router.HeaderCacheControl); len(got) != 0 || len(want) != 0 {
		t.Errorf("Cache-Control: first %q, replay %q, want neither", want, got)
	}
}

func TestIdempotencyUnderAnOuterHTMXRedirect(t *testing.T) {
	var runs atomic.Int32
	r := idempotencyRouter(nil,
		middleware.HTMXRedirect[*appContext],
		middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		runs.Add(1)
		return c.Redirect(http.StatusSeeOther, "/receipt")
	})

	send := func(htmx bool) *httptest.ResponseRecorder {
		req := idempotentRequest(http.MethodPost, "/pay", "k", amount("5"))
		if htmx {
			req.Header.Set(router.HeaderHXRequest, "true")
		}
		return do(r, req)
	}
	for i, htmx := range []bool{true, true, false} {
		rec := send(htmx)
		if htmx && (rec.Code != http.StatusOK || rec.Header().Get(router.HeaderHXRedirect) != "/receipt") {
			t.Errorf("request %d: %d HX-Redirect %q, want 200 to /receipt", i, rec.Code, rec.Header().Get(router.HeaderHXRedirect))
		}
		if !htmx && (rec.Code != http.StatusSeeOther || rec.Header().Get(router.HeaderLocation) != "/receipt") {
			t.Errorf("request %d: %d Location %q, want 303 to /receipt", i, rec.Code, rec.Header().Get(router.HeaderLocation))
		}
	}
	if runs.Load() != 1 {
		t.Errorf("the handler ran %d times, want 1", runs.Load())
	}
}

func TestIdempotencyUnderAnOuterGzip(t *testing.T) {
	body := strings.Repeat("receipt ", 300)
	var runs atomic.Int32
	r := idempotencyRouter(nil,
		middleware.Gzip[*appContext],
		middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		runs.Add(1)
		return c.String(http.StatusCreated, body)
	})

	send := func(encoding string) *httptest.ResponseRecorder {
		req := idempotentRequest(http.MethodPost, "/pay", "k", amount("5"))
		if encoding != "" {
			req.Header.Set(router.HeaderAcceptEncoding, encoding)
		}
		return do(r, req)
	}
	for _, encoding := range []string{"gzip", "", "gzip"} {
		rec := send(encoding)
		got := rec.Body.String()
		if rec.Header().Get(router.HeaderContentEncoding) != encoding {
			t.Fatalf("Accept-Encoding %q: Content-Encoding = %q", encoding, rec.Header().Get(router.HeaderContentEncoding))
		}
		if encoding == "gzip" {
			zr, err := gzip.NewReader(rec.Body)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := io.ReadAll(zr)
			if err != nil {
				t.Fatal(err)
			}
			got = string(plain)
		}
		if rec.Code != http.StatusCreated || got != body {
			t.Errorf("Accept-Encoding %q: %d with a body of %d bytes, want 201 with %d", encoding, rec.Code, len(got), len(body))
		}
	}
	if runs.Load() != 1 {
		t.Errorf("the handler ran %d times, want 1", runs.Load())
	}
}

func TestIdempotencyPassesSafeMethodsAndSkip(t *testing.T) {
	var runs atomic.Int32
	store := newFakeIdempotencyStore()
	r := idempotencyRouter(nil, middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*appContext]{
		Store: store, Skip: skipPath("/webhook"),
	}))
	count := func(c *appContext) error {
		runs.Add(1)
		return c.NoContent(http.StatusOK)
	}
	r.GET("/pay", count)
	r.Handle(router.MethodQuery, "/pay", count)
	r.POST("/webhook", count)

	for _, req := range []*http.Request{
		idempotentRequest(http.MethodGet, "/pay", "k", nil),
		idempotentRequest(http.MethodGet, "/pay", "k", nil),
		idempotentRequest(router.MethodQuery, "/pay", "k", nil),
		idempotentRequest(router.MethodQuery, "/pay", "k", nil),
		idempotentRequest(http.MethodPost, "/webhook", "k", nil),
		idempotentRequest(http.MethodPost, "/webhook", "k", nil),
	} {
		do(r, req)
	}
	if runs.Load() != 6 || store.claims.Load() != 0 {
		t.Errorf("runs = %d, claims = %d; want 6 and 0", runs.Load(), store.claims.Load())
	}
}

func TestIdempotencyRefusesAnOverlongKey(t *testing.T) {
	store := newFakeIdempotencyStore()
	r := idempotencyRouter(nil, middleware.Idempotency(store))
	r.POST("/pay", (&payments{}).pay)

	key := strings.Repeat("k", middleware.MaxIdempotencyKeyLength+1)
	if rec := postKey(r, "/pay", key, amount("5")); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if store.claims.Load() != 0 {
		t.Error("an overlong key reached the store")
	}
	if rec := postKey(r, "/pay", key[1:], amount("5")); rec.Code != http.StatusCreated {
		t.Errorf("a key of the longest length: status = %d, want 201", rec.Code)
	}
}

func TestIdempotencyStoreErrorIsAServerFault(t *testing.T) {
	store := newFakeIdempotencyStore()
	store.claimErr = errors.New("redis at 10.0.0.7 is down")
	r := idempotencyRouter(nil, middleware.Idempotency(store))
	r.POST("/pay", (&payments{}).pay)

	rec := postKey(r, "/pay", "k", amount("5"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("status = %d, body = %q; want 500 without the cause", rec.Code, rec.Body)
	}

	p := &payments{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	full := idempotencyRouter(nil, middleware.Idempotency(
		middleware.NewIdempotencyMemoryStoreWithConfig[*appContext](middleware.IdempotencyMemoryStoreConfig{MaxEntries: 1})))
	full.POST("/pay", p.pay)
	var wg sync.WaitGroup
	wg.Go(func() { postKey(full, "/pay", "a", amount("5")) })
	<-p.entered
	if rec := postKey(full, "/pay", "b", amount("5")); rec.Code != http.StatusInternalServerError {
		t.Errorf("a full store: status = %d, want 500", rec.Code)
	}
	close(p.gate)
	wg.Wait()
}

func multipartBody(t *testing.T, fileContent string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("amount", "5"); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("invoice", "invoice.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(fileContent)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func TestIdempotencyFormFingerprint(t *testing.T) {
	form := func(target, body string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		req.Header.Set(router.HeaderContentType, router.MIMEApplicationForm)
		return req
	}
	upload := func(content string) func(*testing.T) *http.Request {
		return func(t *testing.T) *http.Request {
			body, ct := multipartBody(t, content)
			req := httptest.NewRequest(http.MethodPost, "/pay", body)
			req.Header.Set(router.HeaderContentType, ct)
			return req
		}
	}
	plain := func(target, body string) func(*testing.T) *http.Request {
		return func(*testing.T) *http.Request { return form(target, body) }
	}
	tests := []struct {
		first, repeat func(*testing.T) *http.Request
		name          string
		want          int
	}{
		{name: "reordered form", first: plain("/pay", "amount=5&note=rent"), repeat: plain("/pay", "note=rent&amount=5"), want: http.StatusCreated},
		{name: "another CSRF token", first: plain("/pay", "amount=5&_csrf=a"), repeat: plain("/pay", "amount=5&_csrf=b"), want: http.StatusCreated},
		{name: "another key field", first: plain("/pay", "amount=5&_idempotency_key=a"), repeat: plain("/pay", "amount=5&_idempotency_key=b"), want: http.StatusCreated},
		{name: "another amount", first: plain("/pay", "amount=5"), repeat: plain("/pay", "amount=9"), want: http.StatusUnprocessableEntity},
		{name: "a file of another size", first: upload("12345"), repeat: upload("123456"), want: http.StatusUnprocessableEntity},
		{name: "the same file", first: upload("12345"), repeat: upload("12345"), want: http.StatusCreated},
		// The query and the host are not covered, as the doc says.
		{name: "another query", first: plain("/pay?ref=a", "amount=5"), repeat: plain("/pay?ref=b", "amount=5"), want: http.StatusCreated},
		{name: "another host", first: plain("http://a.example/pay", "amount=5"), repeat: plain("http://b.example/pay", "amount=5"), want: http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var runs atomic.Int32
			r := idempotencyRouter(nil, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
			r.POST("/pay", func(c *appContext) error {
				runs.Add(1)
				return c.NoContent(http.StatusCreated)
			})

			for i, build := range []func(*testing.T) *http.Request{tt.first, tt.repeat} {
				req := build(t)
				req.Header.Set(router.HeaderIdempotencyKey, "k")
				rec := do(r, req)
				want := http.StatusCreated
				if i == 1 {
					want = tt.want
				}
				if rec.Code != want {
					t.Errorf("request %d: status = %d, want %d", i, rec.Code, want)
				}
			}
			if runs.Load() != 1 {
				t.Errorf("the handler ran %d times, want 1", runs.Load())
			}
		})
	}

	b := router.NewBase(httptest.NewRecorder(), form("/pay", "amount=5"))
	if fp, err := middleware.IdempotencyFormFingerprint(b); err != nil || len(fp) != 32 {
		t.Errorf("fingerprint of a bare Base = %x, %v; want 32 bytes", fp, err)
	}
}

func TestIdempotencyFingerprintErrorStopsTheRequest(t *testing.T) {
	store := newFakeIdempotencyStore()
	r := idempotencyRouter(nil, middleware.Idempotency(store))
	r.MaxBodyBytes(16)
	r.POST("/pay", (&payments{}).pay)

	rec := postKey(r, "/pay", "k", url.Values{"note": {strings.Repeat("x", 64)}})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
	if store.claims.Load() != 0 {
		t.Error("a form that did not parse reached the store")
	}
}

func TestIdempotencyWithConfigPanics(t *testing.T) {
	store := middleware.NewIdempotencyMemoryStore[*appContext](0)
	tests := []struct {
		name string
		want string
		cfg  middleware.IdempotencyConfig[*appContext]
	}{
		{name: "no store", want: "needs a Store"},
		{name: "nil source", want: "nil token source", cfg: middleware.IdempotencyConfig[*appContext]{
			Store: store, Sources: []middleware.TokenSource{nil},
		}},
		{name: "too many sources", want: "token sources", cfg: middleware.IdempotencyConfig[*appContext]{
			Store: store, Sources: make([]middleware.TokenSource, middleware.MaxTokenSources+1),
		}},
		{name: "negative MaxBody", want: "MaxBody", cfg: middleware.IdempotencyConfig[*appContext]{
			Store: store, MaxBody: -1,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustPanicContaining(t, tt.want, func() { middleware.IdempotencyWithConfig(tt.cfg) })
		})
	}
	// A negative Wait is a setting: answer 409 at once.
	middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*appContext]{Store: store, Wait: -1})
}

func TestIdempotencyPanicsOnANilStore(t *testing.T) {
	mustPanicContaining(t, "needs a Store", func() { middleware.Idempotency[*appContext](nil) })
}

func TestIdempotencyMemoryStoreRunsOneOfManyRacers(t *testing.T) {
	var runs atomic.Int32
	r := idempotencyRouter(nil, middleware.Idempotency(middleware.NewIdempotencyMemoryStore[*appContext](0)))
	r.POST("/pay", func(c *appContext) error {
		runs.Add(1)
		time.Sleep(20 * time.Millisecond)
		return c.String(http.StatusCreated, "paid "+c.FormValue("amount"))
	})

	codes := make([]int, 32)
	bodies := make([]string, 32)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			rec := postKey(r, "/pay", "k", amount(strconv.Itoa(i%2)))
			codes[i], bodies[i] = rec.Code, rec.Body.String()
		})
	}
	wg.Wait()

	if runs.Load() != 1 {
		t.Fatalf("the handler ran %d times, want 1", runs.Load())
	}
	winner := ""
	for i, code := range codes {
		if code == http.StatusCreated {
			winner = bodies[i]
		}
	}
	for i, code := range codes {
		mine := "paid " + strconv.Itoa(i%2)
		switch {
		case code == http.StatusCreated && bodies[i] == winner && winner == mine:
		case code == http.StatusConflict && winner == mine:
		case code == http.StatusUnprocessableEntity && winner != mine:
		default:
			t.Errorf("racer %d: %d %q with the winner %q", i, code, bodies[i], winner)
		}
	}
}
