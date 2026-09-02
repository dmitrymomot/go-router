package main

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// usersAPI builds the users half of this service as a router of its own. The
// caller decides where it lives, so the prefix appears once, at the mount, and
// never inside a handler. Moving this to its own package changes nothing here.
func usersAPI(apiKey string) *router.Router[*Context] {
	// A mounted router never calls this factory: the root builds the context.
	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
		return new(Context)
	})

	r.GET("/users", listUsers)
	r.GET("/users/{id}", showUser)

	// The reads are open and the writes are not. With gives back a router that
	// carries one more middleware and registers into the same tree.
	write := r.With(requireAPIKey(apiKey))
	write.POST("/users", createUser)
	write.PUT("/users/{id}", replaceUser)
	write.DELETE("/users/{id}", deleteUser)

	return r
}

// requireAPIKey reads the key from "Authorization: Bearer ...", which is the
// default source of KeyAuth.
func requireAPIKey(key string) router.Middleware[*Context] {
	return middleware.KeyAuth(func(_ *Context, sent string) (bool, error) {
		return middleware.SecureCompare(sent, key), nil
	})
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
