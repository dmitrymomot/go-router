package session_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/session"
)

// Row is the session row of the tests, as a sessions table would hold it.
type Row struct {
	ID      string
	UserID  string
	Hash    []byte
	Touched int
}

// table stands in for the sessions table of an application.
type table struct {
	rows    map[string]*Row
	findErr error
	mu      sync.Mutex
}

func newTable() *table { return &table{rows: make(map[string]*Row)} }

func (t *table) insert(userID string, hash []byte) Row {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := &Row{ID: "s" + userID, UserID: userID, Hash: bytes.Clone(hash)}
	t.rows[string(hash)] = r
	return *r
}

func (t *table) find(_ context.Context, hash []byte) (Row, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.findErr != nil {
		return Row{}, t.findErr
	}
	r, ok := t.rows[string(hash)]
	if !ok {
		return Row{}, session.ErrNotFound
	}
	return *r, nil
}

func (t *table) touch(_ context.Context, row Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows[string(row.Hash)].Touched++
	return nil
}

func (t *table) delete(hash []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.rows, string(hash))
}

type appContext struct {
	router.Base
}

// app mounts the routes every test drives: /show reads the session, /login
// starts one for the user named in the query, and /logout ends it.
func app(t *table, sessions *session.Sessions[*appContext, Row]) *router.Router[*appContext] {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(sessions.Middleware())
	r.GET("/show", func(c *appContext) error {
		row, ok := sessions.From(c)
		if !ok {
			return c.String(http.StatusOK, "none")
		}
		return c.String(http.StatusOK, row.UserID)
	})
	r.POST("/login", func(c *appContext) error {
		tok := session.NewToken()
		row := t.insert(c.Request().URL.Query().Get("as"), tok.Hash)
		if err := sessions.Start(c, tok, row, time.Now().Add(time.Hour)); err != nil {
			return err
		}
		// The new row is on the context for the rest of the request.
		if got, ok := sessions.From(c); !ok || got.ID != row.ID {
			return c.String(http.StatusOK, "the old session after Start")
		}
		return c.NoContent(http.StatusNoContent)
	})
	r.POST("/logout", func(c *appContext) error {
		row, ok := sessions.From(c)
		if ok {
			t.delete(row.Hash)
		}
		if err := sessions.End(c); err != nil {
			return err
		}
		if _, still := sessions.From(c); still {
			return c.String(http.StatusOK, "still signed in")
		}
		return c.NoContent(http.StatusNoContent)
	})
	return r
}

func newApp(t *table) *router.Router[*appContext] {
	return app(t, session.New[*appContext](session.Config[Row]{Find: t.find, Touch: t.touch}))
}

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Errorf("did not panic, want a panic containing %q", want)
			return
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Errorf("panic = %v, want it to contain %q", r, want)
		}
	}()
	f()
}

func TestASessionAcrossRequests(t *testing.T) {
	tab := newTable()
	c := routertest.NewClient(t, newApp(tab))

	c.Get("/show").Expect(t).Status(http.StatusOK).Body("none").Header("Vary", "Cookie")

	res := c.Do(http.MethodPost, "/login?as=ada")
	res.Expect(t).Status(http.StatusNoContent)
	ck := res.Cookies()
	if len(ck) != 1 || ck[0].Name != session.DefaultCookie || !ck[0].HttpOnly || ck[0].SameSite != http.SameSiteLaxMode || ck[0].Path != "/" {
		t.Fatalf("Set-Cookie = %v, want one HttpOnly, Lax session cookie at /", ck)
	}
	if ck[0].MaxAge <= 0 || ck[0].MaxAge > 3600 {
		t.Errorf("MaxAge = %d, want it to end with the session", ck[0].MaxAge)
	}

	c.Get("/show").Expect(t).Body("ada")
	c.Get("/show").Expect(t).Body("ada")
	if r := tab.rows[string(session.HashToken(ck[0].Value))]; r == nil || r.Touched != 2 {
		t.Errorf("row = %+v, want it touched by both requests", r)
	}
}

func TestTheTableHoldsOnlyTheHash(t *testing.T) {
	tab := newTable()
	res := routertest.Do(newApp(tab), http.MethodPost, "/login?as=ada")
	secret := res.Cookies()[0].Value
	for k, r := range tab.rows {
		if strings.Contains(k, secret) || bytes.Contains(r.Hash, []byte(secret)) {
			t.Error("the table holds the secret")
		}
		if sum := sha256.Sum256([]byte(secret)); !bytes.Equal(r.Hash, sum[:]) {
			t.Errorf("hash = %x, want the SHA-256 of the secret", r.Hash)
		}
	}
}

func TestEndSignsOut(t *testing.T) {
	tab := newTable()
	c := routertest.NewClient(t, newApp(tab))
	c.Do(http.MethodPost, "/login?as=ada")
	copied := c.Cookie(session.DefaultCookie)

	res := c.Do(http.MethodPost, "/logout")
	res.Expect(t).Status(http.StatusNoContent)
	if ck := res.Cookies(); len(ck) != 1 || ck[0].MaxAge >= 0 {
		t.Errorf("Set-Cookie = %v, want one that clears the cookie", ck)
	}
	c.Get("/show").Expect(t).Body("none")

	// The row is gone, so a copy of the cookie signs nobody in, and the
	// browser is told to drop it.
	stale := routertest.Get(newApp(tab), "/show", routertest.Cookie(copied))
	stale.Expect(t).Body("none")
	if ck := stale.Cookies(); len(ck) != 1 || ck[0].MaxAge >= 0 {
		t.Errorf("Set-Cookie = %v, want the stale cookie cleared", ck)
	}
}

func TestEndWithoutACookieSetsNone(t *testing.T) {
	res := routertest.Do(newApp(newTable()), http.MethodPost, "/logout")
	res.Expect(t).Status(http.StatusNoContent)
	if got := res.Header.Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("Set-Cookie = %v, want none for a visitor", got)
	}
}

func TestEndAfterStartClearsTheNewCookie(t *testing.T) {
	tab := newTable()
	sessions := session.New[*appContext](session.Config[Row]{Find: tab.find})
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.POST("/", func(c *appContext) error {
		if err := sessions.Start(c, session.NewToken(), Row{UserID: "ada"}, time.Now().Add(time.Hour)); err != nil {
			return err
		}
		if err := sessions.End(c); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	if ck := routertest.Do(r, http.MethodPost, "/").Cookies(); len(ck) != 1 || ck[0].MaxAge >= 0 {
		t.Errorf("Set-Cookie = %v, want the cookie of Start replaced by one that clears it", ck)
	}
}

func TestStartRefusesAPastExpiry(t *testing.T) {
	tab := newTable()
	sessions := session.New[*appContext](session.Config[Row]{Find: tab.find})
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	var got error
	r.POST("/", func(c *appContext) error {
		got = sessions.Start(c, session.NewToken(), Row{}, time.Now().Add(-time.Second))
		return c.NoContent(http.StatusNoContent)
	})
	res := routertest.Do(r, http.MethodPost, "/")
	if got == nil || !strings.Contains(got.Error(), "not in the future") || len(res.Cookies()) != 0 {
		t.Errorf("Start = %v with Set-Cookie %v, want an error and no cookie", got, res.Cookies())
	}
}

func TestExpiresEndsARowThatFindKept(t *testing.T) {
	tab := newTable()
	tok := session.NewToken()
	tab.insert("ada", tok.Hash)
	end := time.Now().Add(-time.Minute)
	r := app(tab, session.New[*appContext](session.Config[Row]{
		Find:    tab.find, // forgets the expiry
		Expires: func(Row) time.Time { return end },
	}))
	res := routertest.Get(r, "/show", routertest.Cookie(&http.Cookie{Name: session.DefaultCookie, Value: tok.Secret}))
	res.Expect(t).Body("none")
	if ck := res.Cookies(); len(ck) != 1 || ck[0].MaxAge >= 0 {
		t.Errorf("Set-Cookie = %v, want the expired cookie cleared", ck)
	}
	end = time.Now().Add(time.Hour)
	routertest.Get(r, "/show", routertest.Cookie(&http.Cookie{Name: session.DefaultCookie, Value: tok.Secret})).Expect(t).Body("ada")
}

func TestAnEmptyCookieIsAVisitor(t *testing.T) {
	res := routertest.Get(newApp(newTable()), "/show", routertest.Cookie(&http.Cookie{Name: session.DefaultCookie, Value: ""}))
	res.Expect(t).Body("none")
	if len(res.Cookies()) != 0 {
		t.Errorf("Set-Cookie = %v, want none", res.Cookies())
	}
}

func TestTheHostCookieOverHTTPS(t *testing.T) {
	tab := newTable()
	r := newApp(tab)
	c := routertest.NewClient(t, r, routertest.Header("X-Forwarded-Proto", "https"))

	ck := c.Do(http.MethodPost, "/login?as=ada").Cookies()
	if len(ck) != 1 || ck[0].Name != session.DefaultHostCookie || !ck[0].Secure || ck[0].Path != "/" || ck[0].Domain != "" {
		t.Fatalf("Set-Cookie = %v, want one Secure __Host- cookie at / with no Domain", ck)
	}
	c.Get("/show").Expect(t).Body("ada")

	// A cookie of the HTTP name means nothing over HTTPS, so a parent domain
	// that sets it cannot stand in front of the session.
	tossed := routertest.Get(r, "/show", routertest.Header("X-Forwarded-Proto", "https"),
		routertest.Cookie(&http.Cookie{Name: session.DefaultCookie, Value: ck[0].Value}))
	tossed.Expect(t).Body("none")
}

func TestACookieOfYourOwn(t *testing.T) {
	tab := newTable()
	r := app(tab, session.New[*appContext](session.Config[Row]{Find: tab.find, Cookie: "sid"}))
	c := routertest.NewClient(t, r)
	if ck := c.Do(http.MethodPost, "/login?as=ada").Cookies(); len(ck) != 1 || ck[0].Name != "sid" {
		t.Fatalf("Set-Cookie = %v, want the cookie sid", ck)
	}
	c.Get("/show").Expect(t).Body("ada")
}

func TestStartReplacesAnEarlierCookie(t *testing.T) {
	tab := newTable()
	sessions := session.New[*appContext](session.Config[Row]{Find: tab.find})
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(sessions.Middleware())
	r.POST("/", func(c *appContext) error {
		c.SetCookie(c.NewCookie("theme", "dark", time.Hour))
		if err := sessions.End(c); err != nil {
			return err
		}
		// A session cookie that lasts until the browser closes.
		if err := sessions.Start(c, session.NewToken(), Row{}, time.Time{}); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	ck := routertest.Do(r, http.MethodPost, "/").Cookies()
	if len(ck) != 2 || ck[0].Name != "theme" || ck[1].Name != session.DefaultCookie || ck[1].MaxAge != 0 {
		t.Errorf("Set-Cookie = %v, want theme, then one session cookie with no MaxAge", ck)
	}
}

func TestErrCommitted(t *testing.T) {
	tab := newTable()
	sessions := session.New[*appContext](session.Config[Row]{Find: tab.find})
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(sessions.Middleware())
	var errs []error
	r.GET("/", func(c *appContext) error {
		if err := c.String(http.StatusOK, "streamed"); err != nil {
			return err
		}
		errs = append(errs, sessions.Start(c, session.NewToken(), Row{}, time.Now().Add(time.Hour)), sessions.End(c))
		return nil
	})
	routertest.Get(r, "/").Expect(t).Body("streamed")
	for i, err := range errs {
		if !errors.Is(err, session.ErrCommitted) {
			t.Errorf("call %d = %v, want ErrCommitted", i, err)
		}
	}
}

func TestErrorsOfFindAndTouch(t *testing.T) {
	tab := newTable()
	tok := session.NewToken()
	tab.insert("ada", tok.Hash)
	sid := routertest.Cookie(&http.Cookie{Name: session.DefaultCookie, Value: tok.Secret})

	tab.findErr = errors.New("db down")
	routertest.Get(newApp(tab), "/show", sid).Expect(t).Status(http.StatusInternalServerError)
	tab.findErr = router.ErrForbidden.WithMessage("suspended")
	routertest.Get(newApp(tab), "/show", sid).Expect(t).Status(http.StatusForbidden).Contains("suspended")
	tab.findErr = nil

	r := app(tab, session.New[*appContext](session.Config[Row]{
		Find:  tab.find,
		Touch: func(context.Context, Row) error { return errors.New("db down") },
	}))
	routertest.Get(r, "/show", sid).Expect(t).Status(http.StatusInternalServerError)
}

func TestFromWithoutTheMiddleware(t *testing.T) {
	if _, ok := session.From[Row](context.Background()); ok {
		t.Error("From found a session on a bare context")
	}
}

func TestNewToken(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok := session.NewToken()
		if seen[tok.Secret] || len(tok.Secret) != 43 || !bytes.Equal(tok.Hash, session.HashToken(tok.Secret)) {
			t.Fatalf("NewToken = %+v, want a fresh 43-character secret and its hash", tok)
		}
		seen[tok.Secret] = true
	}
}

func TestPanics(t *testing.T) {
	mustPanic(t, "needs a Find function", func() { session.New[*appContext](session.Config[Row]{}) })
	mustPanic(t, "cannot name a cookie", func() {
		session.New[*appContext](session.Config[Row]{Find: newTable().find, Cookie: "a b"})
	})

	sessions := session.New[*appContext](session.Config[Row]{Find: newTable().find})
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.GET("/", func(c *appContext) error {
		mustPanic(t, "needs a token from NewToken", func() { _ = sessions.Start(c, session.Token{}, Row{}, time.Time{}) })
		return nil
	})
	routertest.Get(r, "/")
}

func cookieNamed(cks []*http.Cookie, name string) *http.Cookie {
	for _, ck := range cks {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func TestFindStartsAnotherSession(t *testing.T) {
	// An impersonation that ended: Find revokes it and starts a session of
	// the admin in its place.
	tab := newTable()
	ended := session.NewToken()
	var sessions *session.Sessions[*appContext, Row]
	sessions = session.New[*appContext](session.Config[Row]{
		Find: func(ctx context.Context, hash []byte) (Row, error) {
			if !bytes.Equal(hash, ended.Hash) {
				return tab.find(ctx, hash)
			}
			c := ctx.(*appContext) // Find runs with the handler context.
			tok := session.NewToken()
			row := tab.insert("admin", tok.Hash)
			//nolint:contextcheck // c is ctx itself, the handler context.
			if err := sessions.Start(c, tok, row, time.Now().Add(time.Hour)); err != nil {
				return Row{}, err
			}
			return row, nil
		},
	})
	r := app(tab, sessions)

	res := routertest.Get(r, "/show", routertest.Cookie(&http.Cookie{Name: session.DefaultCookie, Value: ended.Secret}))
	res.Expect(t).Body("admin")
	main := cookieNamed(res.Cookies(), session.DefaultCookie)
	if main == nil || main.Value == ended.Secret || main.MaxAge <= 0 {
		t.Fatalf("session cookie = %v, want the new session of the admin", main)
	}
	routertest.Get(r, "/show", routertest.Cookie(main)).Expect(t).Body("admin")
}
