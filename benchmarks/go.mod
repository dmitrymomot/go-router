module github.com/dmitrymomot/go-router/benchmarks

go 1.27.0

require (
	github.com/dmitrymomot/go-router v0.0.0-20260827105244-45338d73056e
	github.com/go-chi/chi/v5 v5.3.2
	github.com/labstack/echo/v5 v5.3.1
)

require golang.org/x/net v0.58.0 // indirect

replace github.com/dmitrymomot/go-router => ../
