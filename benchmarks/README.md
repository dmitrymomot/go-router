# Router benchmarks

These results were recorded on 2026-09-01 from `65817fc`, the last commit on this branch that changes anything a request runs, after the router moved its validation to registration, cut the inline parameter array from eight to four, stopped re-escaping the request path on every request, and stopped calling `clear` on a `Base.store` map that is nil.

| Environment | Value |
|---|---|
| Go | `go1.27.0 darwin/arm64` |
| CPU | Apple M3 Max |
| Samples | 10 per benchmark |
| Sample duration | 1 second |

Each comparison sub-benchmark constructs a fresh handler and performs one untimed request before `ResetTimer`. The root benchmarks use the same warm-up. The tables report the median of all ten samples and the full observed range. Allocations were identical in every sample of a row.

## Commands

```bash
env GOPATH=/private/tmp/go-router-bench-gopath GOMODCACHE=/private/tmp/go-router-bench-gopath/pkg/mod GOCACHE=/private/tmp/go-router-bench-gocache go test -run '^$' -bench '^BenchmarkRouters$' -benchmem -benchtime=1s -count=10 .
```

Run from the repository root:

```bash
env GOCACHE=/private/tmp/go-router-docs-gocache go test -run '^$' -bench '^BenchmarkHost(Exact|Param|Any|Fallback)$' -benchmem -benchtime=1s -count=10 .
```

The first command runs from `benchmarks`. The temporary cache paths avoid host cache-permission differences and do not affect the benchmarked code.

## Router comparison

| Case | Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| Static | go-router | 79.0 | 78.5–79.1 | 320 | 1 |
| Static | go-router pooled | 48.1 | 47.9–48.5 | 0 | 0 |
| Static | chi 5.3.2 | 123.1 | 121.8–155.4 | 368 | 2 |
| Static | echo 5.3.1 | 35.5 | 35.2–35.7 | 0 | 0 |
| Static | `http.ServeMux` | 98.4 | 96.9–100.8 | 0 | 0 |
| Parameter | go-router | 85.4 | 84.9–86.2 | 320 | 1 |
| Parameter | go-router pooled | 54.0 | 53.7–54.6 | 0 | 0 |
| Parameter | chi 5.3.2 | 216.8 | 212.0–220.4 | 704 | 4 |
| Parameter | echo 5.3.1 | 43.4 | 43.4–43.6 | 0 | 0 |
| Parameter | `http.ServeMux` | 99.9 | 98.7–101.1 | 16 | 1 |
| Deep | go-router | 116.8 | 115.9–119.1 | 320 | 1 |
| Deep | go-router pooled | 82.9 | 82.2–83.4 | 0 | 0 |
| Deep | chi 5.3.2 | 307.1 | 305.6–321.6 | 704 | 4 |
| Deep | echo 5.3.1 | 70.7 | 69.9–71.7 | 0 | 0 |
| Deep | `http.ServeMux` | 262.7 | 259.8–267.5 | 112 | 3 |

Every go-router range is within 1% of its median. The one wide range in the table is chi's static case, whose worst sample landed 26% over its median; the rest of that column is quiet.

Do not read this table against an older recording. Numbers from two sessions are not comparable, and a previous recording of this table ran under transient scheduler load with ranges as wide as 97.1–592.5 ns.

Measured within one session against `655139c`, the commit before this line of work started, and with ten samples on each side:

| Case | Unpooled | Pooled |
|---|---:|---:|
| Static | -3.2% | +12.3% |
| Parameter | -1.7% | +11.2% |
| Deep | +1.7% | +8.7% |

The pooled rows are slower on purpose, and the cost is two guarantees. About six points of it is `requestPath`, which now scans the path for a percent and a backslash so that `%5C` and `\` stay distinct routes; the old check read `URL.RawPath` and got that wrong. Another two to four points is the cleanup `release` runs before `sync.Pool.Put`, which drops the request, the `ResponseWriter` and the matched parameter strings so a context waiting in the pool holds none of them. Removing both puts the pooled rows within 2% of `655139c`, and removing both is not on offer.

Echo stays 1.4x to 1.6x ahead of the pooled router. A profile of the remaining gap is half trie walk and a sixth `sync.Pool`, with no single item left to remove.

Echo 5.3.1 lands where echo 4.15.4 did on the same machine. The major upgrade changed the API, not the speed.

## Host routing

| Case | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Exact | 94.8 | 94.0–95.7 | 320 | 1 |
| Parameter | 101.4 | 100.0–103.3 | 320 | 1 |
| Wildcard | 92.8 | 91.5–96.2 | 320 | 1 |
| Host-free fallback | 92.9 | 92.1–94.5 | 320 | 1 |

The host measurements include complete authority validation. The pooled measurements include clearing request and response references, alternate parameter storage, caches, deferred state, and per-request values before a context returns to the pool. Those checks intentionally trade a small amount of latency for bounded retention and cross-request isolation while keeping the pooled route path at zero allocations.

## Dependencies

The comparison uses chi 5.3.2 and Echo 5.3.1. Echo 5 makes `Context` a struct, so the benchmark handler takes `*echo.Context` rather than the v4 interface. The upgrade also collapsed the indirect tree: `gommon`, `go-colorable`, `go-isatty`, `fasttemplate`, `bytebufferpool`, `x/crypto`, `x/sys` and `x/text` are gone, leaving `x/net` 0.58.0 as the only indirect dependency.
