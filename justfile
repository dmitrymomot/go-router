# go-router task runner

# Tool versions are pinned here rather than in go.mod. A tool directive would
# put the whole linter dependency tree into the module graph of everyone who
# imports this router, and the library promises no dependencies.
gofumpt := "mvdan.cc/gofumpt@v0.11.0"
goimports := "golang.org/x/tools/cmd/goimports@v0.49.0"
modernize := "golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@v0.49.0"
betteralign := "github.com/dkorunic/betteralign/cmd/betteralign@v0.15.0"
golangci := "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1"
govulncheck := "golang.org/x/vuln/cmd/govulncheck@v1.7.0"

local := "github.com/dmitrymomot/go-router"

# List available recipes
help:
    @just --list

# Run all tests with race detection and coverage
test path='./...':
    go clean -testcache && go test -race -cover {{ path }}

# Report total coverage
cover path='./...':
    go test -coverprofile=coverage.out {{ path }} && go tool cover -func=coverage.out | tail -1

# Run the benchmarks of this module
bench path='./...':
    go clean -testcache && go test -run '^$' -bench=. -benchmem {{ path }}

# Compare against chi, echo and the stdlib mux (separate module, needs network)
bench-compare:
    cd benchmarks && go test -run '^$' -bench=. -benchmem .

# Run every benchmark once, to prove they still compile and run
bench-ci:
    go test -run '^$' -bench=. -benchtime=1x ./...
    cd benchmarks && go test -run '^$' -bench=. -benchtime=1x .

# Format code, imports and struct layout
fmt path='./...':
    #!/usr/bin/env bash
    set -euo pipefail
    go fmt {{ path }}
    go run {{ gofumpt }} -w .
    go run {{ goimports }} -w -local {{ local }} .
    # betteralign exits non-zero when it rewrote something, and one pass can
    # expose the next improvement, so run it until it reports nothing. Only
    # opted-in structs are touched; see the betteralign:check comments on Base
    # and Response.
    for _ in 1 2 3; do
        out="$(go run {{ betteralign }} -opt_in -apply ./... 2>&1 || true)"
        [ -n "$out" ] || break
    done
    gofmt -w .

# The fast checks: types, formatting, and the toolchain modernizers
lint:
    #!/usr/bin/env bash
    set -euo pipefail
    go vet ./...
    go build -o /dev/null ./...
    # gofumpt, goimports and go fix all report on stdout and still exit 0, so
    # turn a non-empty report into a failure.
    #
    # Only stdout counts. "go run tool@version" writes its download progress to
    # stderr, which a cold module cache fills and which says nothing about the
    # code. stderr still reaches the log, and a non-zero exit still fails.
    fail_if_output() {
        local what="$1"; shift
        local out status=0
        out="$("$@")" || status=$?
        if [ "$status" -ne 0 ]; then
            echo "$what: the command exited $status" >&2
            return 1
        fi
        if [ -n "$out" ]; then
            echo "$what:" >&2
            echo "$out" >&2
            return 1
        fi
    }
    fail_if_output "gofumpt would rewrite" go run {{ gofumpt }} -l .
    fail_if_output "goimports would rewrite" go run {{ goimports }} -l -local {{ local }} .
    fail_if_output "go fix has modernizations to apply" go fix -diff ./...
    # The benchmarks are their own module, so the walk above never reaches them.
    (cd benchmarks && go vet ./...)
    # So is every example, and the go tool skips a directory named with a
    # leading underscore as well, so ./... misses them twice over. The build
    # writes to /dev/null: an example is a main package, so a plain "go build"
    # would drop its binary in the working tree on every run.
    for dir in _examples/*/; do
        # An unmatched glob stays literal, and cd would then fail the recipe
        # with a message about a directory that nobody asked for.
        [ -d "$dir" ] || continue
        (
            cd "$dir"
            go build -o /dev/null ./...
            go vet ./...
            fail_if_output "go fix has modernizations to apply in $dir" go fix -diff ./...
        )
    done

# Run the static analyzers: the wider modernizer set, and struct layout
analyze:
    #!/usr/bin/env bash
    set -euo pipefail
    # nilaway is deliberately absent. It reported four findings here and every
    # one was a false positive: three slices that append before they index,
    # and an http.Client.Get result that the caller reaches only after
    # checking its error. Suppressing all four would leave nothing behind.
    go run {{ modernize }} ./...
    go run {{ betteralign }} -opt_in ./...

# Run golangci-lint
golangci:
    # CI runs this recipe rather than the golangci-lint action: the prebuilt
    # binary of the action is built with an older Go, which refuses a module
    # that targets 1.27. Compiling from the pinned version costs a minute and
    # keeps CI and a local run on the same linter.
    go run {{ golangci }} run ./...

# Report known vulnerabilities in the module and the toolchain
vuln:
    go run {{ govulncheck }} ./...

# Everything: format, lint, analyze, golangci-lint, test
check: fmt lint analyze golangci test
