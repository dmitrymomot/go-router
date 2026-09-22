package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// Context is what every handler of this API receives. Whatever a handler
// needs goes here; the router hands the same value to each one.
type Context struct {
	router.Base
	Store *Store
}

// NoUserError is a domain error. StatusCode makes the router answer 404
// without any handler naming a status.
type NoUserError struct{ ID int }

func (e NoUserError) Error() string   { return "no user " + strconv.Itoa(e.ID) }
func (e NoUserError) StatusCode() int { return http.StatusNotFound }

type UserInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Validate runs inside Bind. Field errors become a 422 that names the fields.
func (in UserInput) Validate() error {
	var errs []error
	if in.Name == "" {
		errs = append(errs, router.FieldError{Field: "name", Message: "is required"})
	}
	if !strings.Contains(in.Email, "@") {
		errs = append(errs, router.FieldError{Field: "email", Message: "must be an address"})
	}
	return errors.Join(errs...)
}
