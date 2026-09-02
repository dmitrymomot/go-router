package main

import (
	"net/http"
	"testing"

	"github.com/dmitrymomot/go-router/routertest"
)

func TestEveryRoute(t *testing.T) {
	r := newRouter(NewStore())

	made := routertest.Do(r, http.MethodPost, "/users",
		routertest.JSONBody(UserInput{Name: "ann", Email: "ann@example.com"}))
	made.AssertStatus(t, http.StatusCreated)
	made.AssertBody(t, `{"name":"ann","email":"ann@example.com","id":1}`)

	routertest.Get(r, "/users").AssertStatus(t, http.StatusOK)
	routertest.Get(r, "/users/1").AssertStatus(t, http.StatusOK)

	replaced := routertest.Do(r, http.MethodPut, "/users/1",
		routertest.JSONBody(UserInput{Name: "bob", Email: "bob@example.com"}))
	replaced.AssertBody(t, `{"name":"bob","email":"bob@example.com","id":1}`)

	routertest.Do(r, http.MethodDelete, "/users/1").AssertStatus(t, http.StatusNoContent)
	routertest.Get(r, "/users/1").AssertStatus(t, http.StatusNotFound)
}

func TestValidationNamesTheFields(t *testing.T) {
	r := newRouter(NewStore())

	res := routertest.Do(r, http.MethodPost, "/users",
		routertest.JSONBody(UserInput{Email: "not-an-address"}))
	res.AssertStatus(t, http.StatusUnprocessableEntity)
	res.AssertBody(t,
		`{"error":"Unprocessable Entity","fields":[{"field":"name","message":"is required"},`+
			`{"field":"email","message":"must be an address"}]}`)
}

func TestABadIDIsARejection(t *testing.T) {
	r := newRouter(NewStore())
	routertest.Get(r, "/users/abc").AssertStatus(t, http.StatusBadRequest)
}
