package middleware_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// rateLimitRouter answers one route that the limit guards.
func rateLimitRouter(cfg middleware.RateLimitConfig[*appContext]) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.RateLimitWithConfig[*appContext](cfg))
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, "ok") })
	return r
}

// fakeRateStore answers whatever the test put in it.
type fakeRateStore struct {
	err   error
	wait  time.Duration
	allow bool
}

// Allow implements [middleware.RateLimitStore].
func (s fakeRateStore) Allow(*appContext, string) (bool, time.Duration, error) {
	return s.allow, s.wait, s.err
}

// rateLimitGet asks for the route from one address.
func rateLimitGet(h http.Handler, addr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = addr
	return do(h, req)
}

func TestRateLimitAllowsTheBurstAndThenDenies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rateLimitRouter(middleware.RateLimitConfig[*appContext]{
			Store: middleware.NewMemoryStore[*appContext](1, 3, time.Minute),
		})

		for i := range 3 {
			if rec := get(r, "/"); rec.Code != http.StatusOK {
				t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
			}
		}

		rec := get(r, "/")
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rec.Code)
		}
		if got := rec.Header().Get(router.HeaderRetryAfter); got != "1" {
			t.Errorf("retry after = %q, want 1", got)
		}
	})
}

func TestRateLimitRefillsOverTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rateLimitRouter(middleware.RateLimitConfig[*appContext]{
			Store: middleware.NewMemoryStore[*appContext](1, 1, time.Minute),
		})

		if rec := get(r, "/"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec := get(r, "/"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rec.Code)
		}

		time.Sleep(time.Second)
		if rec := get(r, "/"); rec.Code != http.StatusOK {
			t.Errorf("status = %d after a second of refill, want 200", rec.Code)
		}
	})
}

func TestRateLimitDeniedRequestsTakeNoToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rateLimitRouter(middleware.RateLimitConfig[*appContext]{
			Store: middleware.NewMemoryStore[*appContext](1, 1, time.Minute),
		})

		get(r, "/")
		// A client that keeps knocking through the whole second must not push
		// its own answer further away.
		for range 4 {
			if rec := get(r, "/"); rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", rec.Code)
			}
			time.Sleep(200 * time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)

		if rec := get(r, "/"); rec.Code != http.StatusOK {
			t.Errorf("status = %d a second after the first request, want 200", rec.Code)
		}
	})
}

func TestRateLimitCountsEachClientOnItsOwn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rateLimitRouter(middleware.RateLimitConfig[*appContext]{
			Store: middleware.NewMemoryStore[*appContext](1, 1, time.Minute),
		})

		if rec := rateLimitGet(r, "192.0.2.1:1111"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec := rateLimitGet(r, "192.0.2.1:2222"); rec.Code != http.StatusTooManyRequests {
			t.Errorf("status = %d, want 429: the port is not part of the address", rec.Code)
		}
		if rec := rateLimitGet(r, "198.51.100.7:1111"); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200: another client has its own bucket", rec.Code)
		}
	})
}

func TestMemoryStoreForgetsAClientThatWentQuiet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A rate this low refills a thousandth of a request per second, so a
		// request that passes after the window can only come from a bucket
		// that the store dropped.
		r := rateLimitRouter(middleware.RateLimitConfig[*appContext]{
			Store: middleware.NewMemoryStore[*appContext](0.001, 2, time.Second),
		})

		get(r, "/")
		get(r, "/")
		if rec := get(r, "/"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rec.Code)
		}

		time.Sleep(2 * time.Second)
		if rec := get(r, "/"); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200: the store still holds a client that went quiet", rec.Code)
		}
	})
}

func TestRateLimitKeyFunc(t *testing.T) {
	r := rateLimitRouter(middleware.RateLimitConfig[*appContext]{
		Store:   middleware.NewMemoryStore[*appContext](1, 1, time.Minute),
		KeyFunc: func(c *appContext) (string, error) { return c.Tenant, nil },
	})

	if rec := rateLimitGet(r, "192.0.2.1:1111"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec := rateLimitGet(r, "198.51.100.7:2222"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429: both requests carry the same tenant", rec.Code)
	}
}

func TestRateLimitKeyFuncErrorIsForbidden(t *testing.T) {
	r := rateLimitRouter(middleware.RateLimitConfig[*appContext]{
		Store:   fakeRateStore{allow: true},
		KeyFunc: func(*appContext) (string, error) { return "", errors.New("no tenant") },
	})

	if rec := get(r, "/"); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestRateLimitStoreErrorIsAServerFault(t *testing.T) {
	r := rateLimitRouter(middleware.RateLimitConfig[*appContext]{
		Store: fakeRateStore{err: errors.New("the cache is down")},
	})

	rec := get(r, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "cache is down") {
		t.Errorf("the body leaks the failure of the store: %q", rec.Body.String())
	}
}

func TestRateLimitOnDenyAnswersTheRequest(t *testing.T) {
	var (
		gotID   string
		gotWait time.Duration
	)
	r := rateLimitRouter(middleware.RateLimitConfig[*appContext]{
		Store: fakeRateStore{wait: 3 * time.Second},
		OnDeny: func(c *appContext, id string, retryAfter time.Duration) error {
			gotID, gotWait = id, retryAfter
			return c.String(http.StatusOK, "slow down")
		},
	})

	rec := get(r, "/")
	if rec.Code != http.StatusOK || rec.Body.String() != "slow down" {
		t.Fatalf("status = %d, body = %q, want the answer of OnDeny", rec.Code, rec.Body)
	}
	if gotID != "192.0.2.1" {
		t.Errorf("id = %q, want the client address", gotID)
	}
	if gotWait != 3*time.Second {
		t.Errorf("retry after = %s, want the wait of the store", gotWait)
	}
}

func TestRateLimitPlainFormTakesTheStore(t *testing.T) {
	r := newRouter()
	r.Use(middleware.RateLimit(middleware.NewMemoryStore[*appContext](1, 1, time.Minute)))
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, "ok") })

	if rec := get(r, "/"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec := get(r, "/"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
}

func TestRateLimitSkip(t *testing.T) {
	r := newRouter()
	r.Use(middleware.RateLimitWithConfig[*appContext](middleware.RateLimitConfig[*appContext]{
		Skip:  skipPath("/free"),
		Store: middleware.NewMemoryStore[*appContext](1, 1, time.Minute),
	}))
	r.GET("/free", func(c *appContext) error { return c.String(http.StatusOK, "ok") })

	for i := range 3 {
		if rec := get(r, "/free"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}
}

func TestRateLimitNeedsAStore(t *testing.T) {
	defer func() {
		msg, ok := recover().(string)
		if !ok || !strings.Contains(msg, "Store") {
			t.Errorf("recovered %v, want a panic that names the missing store", msg)
		}
	}()
	middleware.RateLimitWithConfig[*appContext](middleware.RateLimitConfig[*appContext]{})
}

func TestNewMemoryStoreNeedsARate(t *testing.T) {
	defer func() {
		msg, ok := recover().(string)
		if !ok || !strings.Contains(msg, "rate") {
			t.Errorf("recovered %v, want a panic that names the rate", msg)
		}
	}()
	middleware.NewMemoryStore[*appContext](0, 1, time.Minute)
}
