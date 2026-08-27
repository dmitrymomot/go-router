# go-router task runner

# List available recipes
help:
    @just --list

# Run all tests with race detection and coverage
test path='./...':
    go clean -testcache && go test -race -cover {{ path }}

# Report coverage per package
cover path='./...':
    go test -coverprofile=coverage.out {{ path }} && go tool cover -func=coverage.out | tail -1

# Run the benchmarks of this module
bench path='./...':
    go clean -testcache && go test -run '^$' -bench=. -benchmem {{ path }}

# Compare against chi, echo and the stdlib mux (separate module, needs network)
bench-compare:
    cd benchmarks && go test -run '^$' -bench=. -benchmem .

# Run code linters
lint:
    #!/usr/bin/env bash
    set -euo pipefail
    go vet ./...
    go build -o /dev/null ./...
    # gofmt and go fix report their findings on stdout and still exit 0, so
    # turn a non-empty report into a failure.
    unformatted="$(gofmt -l .)"
    if [ -n "$unformatted" ]; then
        echo "gofmt would rewrite:" >&2
        echo "$unformatted" >&2
        exit 1
    fi
    outdated="$(go fix -diff ./... 2>&1)"
    if [ -n "$outdated" ]; then
        echo "go fix has modernizations to apply:" >&2
        echo "$outdated" >&2
        exit 1
    fi
    # The benchmarks are their own module, so the walk above never reaches them.
    cd benchmarks && go vet ./...

# Format code
fmt path='./...':
    go fmt {{ path }}

# Report known vulnerabilities in the module and the toolchain
vuln:
    go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Run fmt, lint, and test
check: fmt lint test

# Run every benchmark once, to prove they still compile and run
bench-ci:
    go test -run '^$' -bench=. -benchtime=1x ./...
    cd benchmarks && go test -run '^$' -bench=. -benchtime=1x .
