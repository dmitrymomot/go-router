# Router benchmarks

These results were recorded on 2026-09-01 from commit `f5149f8`, after the
router moved its validation to registration and cut the inline parameter array
from eight to four.

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
| Static | go-router | 86.6 | 86.1–87.2 | 320 | 1 |
| Static | go-router pooled | 57.0 | 56.8–57.5 | 0 | 0 |
| Static | chi 5.3.2 | 121.1 | 120.1–121.5 | 368 | 2 |
| Static | echo 5.3.1 | 35.4 | 35.3–35.5 | 0 | 0 |
| Static | `http.ServeMux` | 98.0 | 97.7–99.1 | 0 | 0 |
| Parameter | go-router | 93.0 | 92.5–94.0 | 320 | 1 |
| Parameter | go-router pooled | 62.3 | 62.1–64.2 | 0 | 0 |
| Parameter | chi 5.3.2 | 214.8 | 212.2–217.4 | 704 | 4 |
| Parameter | echo 5.3.1 | 43.9 | 43.8–44.2 | 0 | 0 |
| Parameter | `http.ServeMux` | 100.4 | 99.9–101.9 | 16 | 1 |
| Deep | go-router | 135.6 | 134.9–137.4 | 320 | 1 |
| Deep | go-router pooled | 101.1 | 100.7–102.1 | 0 | 0 |
| Deep | chi 5.3.2 | 312.6 | 308.2–314.4 | 704 | 4 |
| Deep | echo 5.3.1 | 71.4 | 71.0–72.9 | 0 | 0 |
| Deep | `http.ServeMux` | 262.7 | 260.9–265.9 | 112 | 3 |

This run was quiet: every range is within 2% of its median. Do not read the
table against the previous recording, which ran under transient scheduler load
and reported ranges as wide as 97.1–592.5 ns. Numbers from two sessions are not
comparable. Measured within one session against the pre-refactor commit, the
unpooled cases moved -3.3%, -4.4% and -2.4%, and the pooled cases +1.5%, +1.5%
and +1.3%, which is the run-to-run band for this suite.

Echo 5.3.1 lands where echo 4.15.4 did on the same machine. The major upgrade
changed the API, not the speed.

## Host routing

| Case | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Exact | 103.5 | 102.5–105.1 | 320 | 1 |
| Parameter | 108.2 | 107.2–111.7 | 320 | 1 |
| Wildcard | 99.4 | 99.0–102.6 | 320 | 1 |
| Host-free fallback | 99.9 | 98.8–100.4 | 320 | 1 |

The host measurements include complete authority validation. The pooled
measurements include clearing request and response references, alternate
parameter storage, caches, deferred state, and per-request values before a
context returns to the pool. Those checks intentionally trade a small amount of
latency for bounded retention and cross-request isolation while keeping the
pooled route path at zero allocations.

## Dependencies

The comparison uses chi 5.3.2 and Echo 5.3.1. Echo 5 makes `Context` a struct,
so the benchmark handler takes `*echo.Context` rather than the v4 interface.
The upgrade also collapsed the indirect tree: `gommon`, `go-colorable`,
`go-isatty`, `fasttemplate`, `bytebufferpool`, `x/crypto`, `x/sys` and `x/text`
are gone, leaving `x/net` 0.58.0 as the only indirect dependency.
