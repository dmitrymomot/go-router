# go-router task runner

# Pinned here and not in go.mod: a tool directive would put the linter tree
# into the module graph of every importer.
gofumpt := "mvdan.cc/gofumpt@v0.11.0"
goimports := "golang.org/x/tools/cmd/goimports@v0.49.0"
modernize := "golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@v0.49.0"
betteralign := "github.com/dkorunic/betteralign/cmd/betteralign@v0.15.0"
golangci := "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2"
govulncheck := "golang.org/x/vuln/cmd/govulncheck@v1.7.0"
actionlint := "github.com/rhysd/actionlint/cmd/actionlint@v1.7.12"

local := "github.com/dmitrymomot/go-router"

# List available recipes
help:
    @just --list

# Run all tests with race detection and coverage
test path='./...':
    set -eu; \
    go test -count=1 {{ path }}; \
    go test -race -cover -count=1 {{ path }}; \
    ( \
        cd benchmarks; \
        go test -count=1 ./...; \
        go test -race -cover -count=1 ./... \
    ); \
    ( \
        cd _examples/chat; \
        go test -count=1 ./...; \
        go test -race -cover -count=1 ./... \
    )

# Report total coverage
cover:
    set -eu; \
    profile="$(mktemp "${TMPDIR:-/tmp}/go-router-coverage.XXXXXX")"; \
    trap 'rm -f "$profile"' 0; \
    go test -count=1 -coverprofile="$profile" ./...; \
    report="$(go tool cover -func="$profile")"; \
    total="$(printf '%s\n' "$report" | awk '/^total:/ { gsub(/%/, "", $3); print $3 }')"; \
    printf 'total coverage: %s%%\n' "$total"; \
    awk -v total="$total" 'BEGIN { exit !(total + 0 >= 95) }'

# Run the benchmarks of this module
bench path='./...':
    go test -count=1 -run '^$' -bench=. -benchmem {{ path }}

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
    # expose the next, so loop until it reports nothing.
    for _ in 1 2 3; do
        out="$(go run {{ betteralign }} -opt_in -apply ./... 2>&1 || true)"
        [ -n "$out" ] || break
    done
    gofmt -w .

fmt-check:
    set -eu; \
    fail_if_output() { \
        what="$1"; shift; \
        out=; status=0; \
        out="$("$@")" || status=$?; \
        if [ "$status" -ne 0 ]; then \
            echo "$what: the command exited $status" >&2; \
            [ -z "$out" ] || echo "$out" >&2; \
            return 1; \
        fi; \
        if [ -n "$out" ]; then \
            echo "$what:" >&2; \
            echo "$out" >&2; \
            return 1; \
        fi; \
    }; \
    fail_if_output "gofmt would rewrite" gofmt -l .; \
    fail_if_output "gofumpt would rewrite" go run {{ gofumpt }} -l .; \
    fail_if_output "goimports would rewrite" go run {{ goimports }} -l -local {{ local }} .

# The fast checks: types, formatting, and the toolchain modernizers
lint: fmt-check
    #!/usr/bin/env bash
    set -euo pipefail
    go vet ./...
    go build -o /dev/null ./...
    go mod tidy -diff
    # gofumpt, goimports and go fix report on stdout and still exit 0, so a
    # non-empty report has to become a failure. Only stdout counts: "go run
    # tool@version" writes download progress to stderr.
    fail_if_output() {
        local what="$1"; shift
        local out status=0
        out="$("$@")" || status=$?
        if [ "$status" -ne 0 ]; then
            echo "$what: the command exited $status" >&2
            [ -z "$out" ] || echo "$out" >&2
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
    (
        cd benchmarks
        go build -o /dev/null ./...
        go vet ./...
        go mod tidy -diff
        fail_if_output "go fix has modernizations to apply in benchmarks" go fix -diff ./...
    )
    # So is every example, and ./... skips a leading underscore too. The build
    # writes to /dev/null so no binary lands in the working tree.
    for dir in _examples/*/; do
        # An unmatched glob stays literal, and cd would fail the recipe.
        [ -d "$dir" ] || continue
        (
            cd "$dir"
            go build -o /dev/null ./...
            go vet ./...
            go mod tidy -diff
            fail_if_output "go fix has modernizations to apply in $dir" go fix -diff ./...
        )
    done

# Run the static analyzers: the wider modernizer set, and struct layout
analyze:
    #!/usr/bin/env bash
    set -euo pipefail
    # nilaway is deliberately absent: all four of its findings here were false
    # positives, and suppressing them would leave nothing behind.
    for dir in . benchmarks _examples/chat; do
        (
            cd "$dir"
            go run {{ modernize }} ./...
            go run {{ betteralign }} -opt_in ./...
        )
    done

# Run golangci-lint
golangci:
    # CI runs this recipe and not the action: the prebuilt binary of the action
    # is built with an older Go, which refuses a module targeting 1.27.
    set -eu; \
    for dir in . benchmarks _examples/chat; do \
        (cd "$dir" && go run {{ golangci }} run ./...); \
    done

# Report known vulnerabilities in the module and the toolchain
vuln:
    set -eu; \
    for dir in . benchmarks _examples/chat; do \
        (cd "$dir" && go run {{ govulncheck }} ./...); \
    done

actionlint:
    go run {{ actionlint }}

fuzz-smoke:
    set -eu; \
    for dir in . benchmarks _examples/chat; do \
        (cd "$dir" && go test -count=1 -run '^Fuzz' ./...); \
    done

cross:
    set -eu; \
    for dir in . benchmarks _examples/chat; do \
        (cd "$dir" && env GOOS=linux GOARCH=386 CGO_ENABLED=0 go test -exec=true -count=1 ./...); \
    done

clean-cache:
    go clean -cache

# Everything: format, lint, analyze, golangci-lint, test
check: clean-cache fmt-check lint analyze golangci test cover vuln actionlint fuzz-smoke cross
