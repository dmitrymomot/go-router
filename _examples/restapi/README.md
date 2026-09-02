# restapi

A JSON API in about 300 lines: a mounted sub-router, binding with validation, domain errors that carry their own status, one error handler that answers JSON, and a server that drains on Ctrl-C.

```bash
go run .
```

It prints its route table, and the API key it made for this run when `API_KEY` is unset. The reads are open; the writes want that key.

```bash
curl -s localhost:8080/v1/users
```

```bash
curl -s -X POST localhost:8080/v1/users -H 'Authorization: Bearer <key>' -H 'Content-Type: application/json' -d '{"name":"ann","email":"ann@example.com"}'
```

The content type decides how `Bind` reads the body, so a POST without it is read as a form and answered `422`.

Nothing is stored. The users live in a map that goes away with the process.

## The routes

| Request | Key | Answer |
| --- | --- | --- |
| `GET /healthz` | no | `204`, and no rate limit |
| `GET /v1/users` | no | every user, ordered by id |
| `POST /v1/users` | yes | `201` and the new user, or `422` and the fields that failed |
| `GET /v1/users/{id}` | no | the user, or `404` |
| `PUT /v1/users/{id}` | yes | the replaced user, or `404` |
| `DELETE /v1/users/{id}` | yes | `204`, or `404` |

## The five pieces worth copying

**A version that appears once.** `usersAPI` is a router of its own, so no handler knows where it lives and a v2 is one more line:

```go
r.Mount("/v1", usersAPI(apiKey))
```

**Middleware on some routes, not all.** `With` gives back a router that carries one more middleware and registers into the same tree:

```go
write := r.With(requireAPIKey(apiKey))
write.POST("/users", createUser)
```

**Validation that names the field.** `Bind` calls `Validate` after it decodes. Joined `router.FieldError` values become a `422` whose `Details` list them, and the error handler puts them in the body:

```go
func (in UserInput) Validate() error {
	var errs []error
	if in.Name == "" {
		errs = append(errs, router.FieldError{Field: "name", Message: "is required"})
	}
	return errors.Join(errs...)
}
```

**A domain error that knows its status.** Any error with a `StatusCode() int` method sets the status, so a handler returns its own type and never repeats `http.StatusNotFound`:

```go
func (e NoUserError) StatusCode() int { return http.StatusNotFound }
```

**One place that writes the failures.** The router answers errors in plain text by default. An API says so once, in `ErrorHandler`, and every handler after that just returns an error:

```go
r.ErrorHandler(writeError)
```

`ErrorHandler` and `MaxBodyBytes` belong to the root router. `Mount` refuses a sub-router that carries them, because there is one answer to give and one body limit to enforce.

## The files

| File | Holds |
| --- | --- |
| `main.go` | the root router: the settings, the middleware stack, the health route and the mount |
| `users.go` | the mounted router, the key check and the five handlers |
| `api.go` | the context type, the input type, the domain error and the error writer |
| `store.go` | the users, in a map behind a mutex |

## What a real API would add

A database, per-client keys instead of one, pagination on the list, and `PATCH` beside `PUT`. The rate limit counts one client per IP, which is wrong the moment two clients share one.
