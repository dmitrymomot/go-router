# Router benchmarks

These results were recorded on 2026-08-28 from the worktree based on commit
`655139c3f9b9`. The worktree included the security and correctness changes in
the current branch.

| Environment | Value |
|---|---|
| Go | `go1.27.0 darwin/arm64` |
| CPU | Apple M3 Max |
| Samples | 10 per benchmark |
| Sample duration | 1 second |

Each comparison sub-benchmark constructs a fresh handler and performs one
untimed request before `ResetTimer`. The root benchmarks use the same warm-up.
The tables report the median of all ten samples and the full observed range.
Allocations were identical in every sample of a row.

## Commands

```bash
env GOPATH=/private/tmp/go-router-bench-gopath GOMODCACHE=/private/tmp/go-router-bench-gopath/pkg/mod GOCACHE=/private/tmp/go-router-bench-gocache go test -run '^$' -bench '^BenchmarkRouters$' -benchmem -benchtime=1s -count=10 .
```

Run from the repository root:

```bash
env GOCACHE=/private/tmp/go-router-docs-gocache go test -run '^$' -bench '^BenchmarkHost(Exact|Param|Any|Fallback)$' -benchmem -benchtime=1s -count=10 .
```

The first command runs from `benchmarks`. The temporary cache paths avoid host
cache-permission differences and do not affect the benchmarked code.

## Router comparison

| Case | Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| Static | go-router | 108.6 | 97.1–592.5 | 384 | 1 |
| Static | go-router pooled | 48.1 | 47.3–51.8 | 0 | 0 |
| Static | chi 5.3.2 | 146.9 | 137.7–364.5 | 368 | 2 |
| Static | echo 4.15.4 | 33.4 | 32.9–47.2 | 0 | 0 |
| Static | `http.ServeMux` | 102.6 | 101.4–107.0 | 0 | 0 |
| Parameter | go-router | 131.7 | 101.4–640.4 | 384 | 1 |
| Parameter | go-router pooled | 54.3 | 53.2–93.4 | 0 | 0 |
| Parameter | chi 5.3.2 | 243.8 | 241.0–249.8 | 704 | 4 |
| Parameter | echo 4.15.4 | 40.4 | 39.5–42.1 | 0 | 0 |
| Parameter | `http.ServeMux` | 106.2 | 104.9–120.1 | 16 | 1 |
| Deep | go-router | 131.9 | 130.3–142.9 | 384 | 1 |
| Deep | go-router pooled | 81.8 | 81.2–83.0 | 0 | 0 |
| Deep | chi 5.3.2 | 347.6 | 345.4–353.8 | 704 | 4 |
| Deep | echo 4.15.4 | 68.3 | 67.8–68.7 | 0 | 0 |
| Deep | `http.ServeMux` | 276.7 | 272.6–279.2 | 112 | 3 |

Transient scheduler load produced several wide ranges near the start of the
comparison run. No sample was discarded. The medians summarize that finite run;
use the commands above on the deployment-class machine that matters to you.

## Host routing

| Case | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Exact | 112.4 | 106.1–120.7 | 384 | 1 |
| Parameter | 116.2 | 110.3–139.7 | 384 | 1 |
| Wildcard | 111.8 | 102.1–135.5 | 384 | 1 |
| Host-free fallback | 103.4 | 102.5–131.9 | 384 | 1 |

The host measurements include complete authority validation. The pooled
measurements include clearing request and response references, alternate
parameter storage, caches, deferred state, and per-request values before a
context returns to the pool. Those checks intentionally trade a small amount of
latency for bounded retention and cross-request isolation while keeping the
pooled route path at zero allocations.

## Dependencies

The comparison uses chi 5.3.2 and Echo 4.15.4. The safe update kept Echo on its
existing major version and updated the indirect benchmark dependencies to
`go-isatty` 0.0.24, `x/crypto` 0.55.0, `x/net` 0.58.0, `x/sys` 0.47.0, and
`x/text` 0.41.0. Moving the comparison to Echo 5 would change the benchmarked
major API and is intentionally left to a separate methodology change.
