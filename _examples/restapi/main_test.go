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

	routertest.Get(r, "/healthz").AssertStatus(t, http.StatusNoContent)

	made := routertest.Do(r, http.MethodPost, "/v1/users", bearer(),
		routertest.JSONBody(UserInput{Name: "ann", Email: "ann@example.com"}))
	made.AssertStatus(t, http.StatusCreated)
	made.AssertBody(t, `{"name":"ann","email":"ann@example.com","id":1}`)

	routertest.Get(r, "/v1/users").AssertStatus(t, http.StatusOK)
	routertest.Get(r, "/v1/users/1").AssertStatus(t, http.StatusOK)

	replaced := routertest.Do(r, http.MethodPut, "/v1/users/1", bearer(),
		routertest.JSONBody(UserInput{Name: "bob", Email: "bob@example.com"}))
	replaced.AssertBody(t, `{"name":"bob","email":"bob@example.com","id":1}`)

	routertest.Do(r, http.MethodDelete, "/v1/users/1", bearer()).
		AssertStatus(t, http.StatusNoContent)
	routertest.Get(r, "/v1/users/1").AssertStatus(t, http.StatusNotFound)
}

func TestOnlyTheWritesNeedTheKey(t *testing.T) {
	r := newRouter(NewStore(), testKey)

	routertest.Get(r, "/v1/users").AssertStatus(t, http.StatusOK)

	res := routertest.Do(r, http.MethodPost, "/v1/users",
		routertest.JSONBody(UserInput{Name: "ann", Email: "ann@example.com"}))
	res.AssertStatus(t, http.StatusUnauthorized)
	res.AssertHeader(t, router.HeaderWWWAuthenticate, "Bearer")
}

func TestValidationNamesTheFields(t *testing.T) {
	r := newRouter(NewStore(), testKey)

	res := routertest.Do(r, http.MethodPost, "/v1/users", bearer(),
		routertest.JSONBody(UserInput{Email: "not-an-address"}))
	res.AssertStatus(t, http.StatusUnprocessableEntity)

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
	res.AssertStatus(t, http.StatusNotFound)
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
	routertest.Get(r, "/v1/users/abc").AssertStatus(t, http.StatusNotFound)
}

func TestTheMountAppearsInTheRouteTable(t *testing.T) {
	r := newRouter(NewStore(), testKey)

	want := "GET /v1/users/{id}"
	for _, rt := range r.Routes() {
		if rt.Method+" "+rt.Pattern == want {
			return
		}
	}
	t.Fatalf("no %q in the route table: %v", want, r.Routes())
}
