package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

const testKey = "test-key"

func bearer() routertest.RequestOption {
	return routertest.Header(router.HeaderAuthorization, "Bearer "+testKey)
}

func TestEveryRoute(t *testing.T) {
	r := newRouter(NewStore(), testKey)

	routertest.Get(r, "/healthz").Expect(t).Status(http.StatusNoContent)

	made := routertest.Do(r, http.MethodPost, "/v1/users", bearer(),
		routertest.JSONBody(UserInput{Name: "ann", Email: "ann@example.com"}))
	made.Expect(t).Status(http.StatusCreated).Body(`{"name":"ann","email":"ann@example.com","id":1}`)

	routertest.Get(r, "/v1/users").Expect(t).Status(http.StatusOK)
	routertest.Get(r, "/v1/users/1").Expect(t).Status(http.StatusOK)

	replaced := routertest.Do(r, http.MethodPut, "/v1/users/1", bearer(),
		routertest.JSONBody(UserInput{Name: "bob", Email: "bob@example.com"}))
	replaced.Expect(t).Body(`{"name":"bob","email":"bob@example.com","id":1}`)

	routertest.Do(r, http.MethodDelete, "/v1/users/1", bearer()).
		Expect(t).Status(http.StatusNoContent)
	routertest.Get(r, "/v1/users/1").Expect(t).Status(http.StatusNotFound)
}

func TestOnlyTheWritesNeedTheKey(t *testing.T) {
	r := newRouter(NewStore(), testKey)

	routertest.Get(r, "/v1/users").Expect(t).Status(http.StatusOK)

	res := routertest.Do(r, http.MethodPost, "/v1/users",
		routertest.JSONBody(UserInput{Name: "ann", Email: "ann@example.com"}))
	res.Expect(t).Status(http.StatusUnauthorized).Header(router.HeaderWWWAuthenticate, "Bearer")
}

func TestValidationNamesTheFields(t *testing.T) {
	r := newRouter(NewStore(), testKey)

	res := routertest.Do(r, http.MethodPost, "/v1/users", bearer(),
		routertest.JSONBody(UserInput{Email: "not-an-address"}))
	res.Expect(t).Status(http.StatusUnprocessableEntity)

	body, err := res.ErrorBody()
	if err != nil {
		t.Fatal(err)
	}
	fields, ok := body.Details.([]router.FieldError)
	if body.Status != http.StatusUnprocessableEntity || !ok {
		t.Fatalf("body = %+v, want a 422 that names the fields", body)
	}
	var names []string
	for _, f := range fields {
		names = append(names, f.Field)
	}
	if !slices.Equal(names, []string{"name", "email"}) {
		t.Errorf("fields = %v, want [name email]", names)
	}
}

func TestAMissingUserKeepsItsTextOnTheServer(t *testing.T) {
	r := newRouter(NewStore(), testKey)

	res := routertest.Get(r, "/v1/users/9")
	res.Expect(t).Status(http.StatusNotFound)
	body, err := res.ErrorBody()
	if err != nil {
		t.Fatal(err)
	}
	if body.Message != "Not Found" || strings.Contains(res.String(), "no user") {
		t.Errorf("body = %s, want the standard text and not the error of the store", res.Body)
	}
}

func TestABadIDIsNotFound(t *testing.T) {
	r := newRouter(NewStore(), testKey)
	routertest.Get(r, "/v1/users/abc").Expect(t).Status(http.StatusNotFound)
}

func TestTheMountAppearsInTheRouteTable(t *testing.T) {
	r := newRouter(NewStore(), testKey)

	want := "GET /v1/users/{id:int}"
	for _, rt := range r.Routes() {
		if rt.Method+" "+rt.Pattern == want {
			return
		}
	}
	t.Fatalf("no %q in the route table: %v", want, r.Routes())
}

func TestTheHealthRouteIsNeverRateLimited(t *testing.T) {
	r := newRouter(NewStore(), testKey)

	for i := range 30 {
		if res := routertest.Get(r, "/healthz"); res.StatusCode != http.StatusNoContent {
			t.Fatalf("health check %d answered %d, want 204", i+1, res.StatusCode)
		}
	}
	limited := false
	for range 30 {
		if routertest.Get(r, "/v1/users").StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("30 requests to /v1/users met no rate limit, so the health test proves nothing")
	}
}
