# restapi

A JSON API in about 260 lines: binding with validation, domain errors that carry their own status, one error handler that answers JSON, and a server that drains on Ctrl-C.

```bash
go run .
```

```bash
curl -s -X POST localhost:8080/users -d '{"name":"ann","email":"ann@example.com"}'
```

Nothing is stored. The users live in a map that goes away with the process.

## The routes

| Request | Answer |
| --- | --- |
| `GET /users` | every user, ordered by id |
| `POST /users` | `201` and the new user, or `422` and the fields that failed |
| `GET /users/{id}` | the user, or `404` |
| `PUT /users/{id}` | the replaced user, or `404` |
| `DELETE /users/{id}` | `204`, or `404` |

## The four pieces worth copying

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

**A server that stops on its own.** `serve.Run` returns when the context ends, after the open requests finish:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
err := serve.Run(ctx, newRouter(NewStore()), serve.Config{Addr: ":8080"})
```

## The files

| File | Holds |
| --- | --- |
| `main.go` | the middleware stack, the error handler and the server |
| `api.go` | the context type, the input type, the domain error and the handlers |
| `store.go` | the users, in a map behind a mutex |

## What a real API would add

A database, authentication in front of the writes, pagination on the list, and `PATCH` beside `PUT`. The rate limit counts one client per IP, which is wrong the moment two clients share one.
