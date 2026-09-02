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

// UserInput is the body of a create and of an update.
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

func routes(r *router.Router[*Context]) {
	r.GET("/users", listUsers)
	r.POST("/users", createUser)
	r.GET("/users/{id}", showUser)
	r.PUT("/users/{id}", replaceUser)
	r.DELETE("/users/{id}", deleteUser)
}

func listUsers(c *Context) error {
	return c.JSON(http.StatusOK, c.Store.List())
}

func createUser(c *Context) error {
	in, err := c.Bind[UserInput]()
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, c.Store.Create(in.Name, in.Email))
}

func showUser(c *Context) error {
	// ParamAs parses the segment, and answers 400 when it is not a number.
	id, err := c.ParamAs[int]("id")
	if err != nil {
		return err
	}
	u, ok := c.Store.Get(id)
	if !ok {
		return NoUserError{ID: id}
	}
	return c.JSON(http.StatusOK, u)
}

func replaceUser(c *Context) error {
	id, err := c.ParamAs[int]("id")
	if err != nil {
		return err
	}
	in, err := c.Bind[UserInput]()
	if err != nil {
		return err
	}
	u, ok := c.Store.Update(id, in.Name, in.Email)
	if !ok {
		return NoUserError{ID: id}
	}
	return c.JSON(http.StatusOK, u)
}

func deleteUser(c *Context) error {
	id, err := c.ParamAs[int]("id")
	if err != nil {
		return err
	}
	if !c.Store.Delete(id) {
		return NoUserError{ID: id}
	}
	return c.NoContent(http.StatusNoContent)
}

// ErrorBody is what a client reads when a request fails.
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
