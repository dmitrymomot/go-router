package main

import (
	"cmp"
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

type ErrorBody struct {
	Error  string              `json:"error"`
	Fields []router.FieldError `json:"fields,omitempty"`
}

// writeError answers every failure as JSON. The router writes plain text by
// default, which no API client wants to parse.
func writeError(c *Context, err error) {
	status := router.StatusOf(err)
	body := ErrorBody{Error: err.Error()}

	if he, ok := errors.AsType[*router.HTTPError](err); ok {
		body.Error = cmp.Or(he.Message, http.StatusText(status))
		// Bind puts the failed fields here, and they are the useful half.
		if fields, ok := he.Details.([]router.FieldError); ok {
			body.Fields = fields
		}
	}

	//nolint:errcheck // The request is already failing; nowhere left to report.
	c.JSON(status, body)
}
