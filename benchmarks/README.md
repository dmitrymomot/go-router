# Router benchmarks

These results were recorded on 2026-09-01 from commit `846504b` plus the
request-path fast path, after the router moved its validation to registration,
cut the inline parameter array from eight to four, and stopped re-escaping the
request path on every request.

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
| Static | go-router | 80.8 | 80.0–81.5 | 320 | 1 |
| Static | go-router pooled | 50.2 | 50.0–51.0 | 0 | 0 |
| Static | chi 5.3.2 | 120.7 | 120.3–123.3 | 368 | 2 |
| Static | echo 5.3.1 | 35.9 | 35.3–36.7 | 0 | 0 |
| Static | `http.ServeMux` | 98.2 | 97.7–100.3 | 0 | 0 |
| Parameter | go-router | 85.3 | 84.8–86.7 | 320 | 1 |
| Parameter | go-router pooled | 55.4 | 55.3–55.8 | 0 | 0 |
| Parameter | chi 5.3.2 | 210.2 | 207.0–225.3 | 704 | 4 |
| Parameter | echo 5.3.1 | 43.6 | 43.3–45.1 | 0 | 0 |
| Parameter | `http.ServeMux` | 100.1 | 99.3–102.6 | 16 | 1 |
| Deep | go-router | 115.4 | 114.8–117.1 | 320 | 1 |
| Deep | go-router pooled | 84.1 | 83.6–85.2 | 0 | 0 |
| Deep | chi 5.3.2 | 300.5 | 298.3–305.1 | 704 | 4 |
| Deep | echo 5.3.1 | 70.5 | 69.8–71.2 | 0 | 0 |
| Deep | `http.ServeMux` | 256.4 | 254.8–259.0 | 112 | 3 |

This run was quiet: every range is within 2% of its median. Do not read the
table against the previous recording, which ran under transient scheduler load
and reported ranges as wide as 97.1–592.5 ns. Numbers from two sessions are not
comparable. Measured within one session against the pre-refactor commit, the
unpooled cases moved -3.3%, -4.4% and -2.4%, and the pooled cases +1.5%, +1.5%
and +1.3%, which is the run-to-run band for this suite. The request-path fast
path then took another -12.25% geomean off every go-router row.

Echo stays 1.4x to 1.6x ahead of the pooled router. A profile of the remaining
gap is half trie walk and a sixth `sync.Pool`, with no single item left to
remove.

Echo 5.3.1 lands where echo 4.15.4 did on the same machine. The major upgrade
changed the API, not the speed.

## Host routing

| Case | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Exact | 93.9 | 92.8–95.6 | 320 | 1 |
| Parameter | 99.0 | 98.7–99.7 | 320 | 1 |
| Wildcard | 91.0 | 90.7–91.5 | 320 | 1 |
| Host-free fallback | 92.2 | 91.9–96.7 | 320 | 1 |

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
