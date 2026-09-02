# Router benchmarks

These results were recorded on 2026-09-02 from `385c40d`, in one session, after the router moved its validation to registration, cut the inline parameter array from eight to four, stopped re-escaping the request path on every request, and stopped calling `clear` on a `Base.store` map that is nil.

| Environment | Value |
|---|---|
| Go | `go1.27.0 darwin/arm64` |
| CPU | Apple M3 Max |
| Samples | 10 per benchmark |
| Sample duration | 1 second |

Each comparison sub-benchmark constructs a fresh handler and performs one untimed request before `ResetTimer`. The root benchmarks use the same warm-up. The tables report the median of all ten samples and the full observed range. Allocations were identical in every sample of a row.

`TestCasesMatch` asserts that all five routers answer the parameter, five-parameter and scale targets with the expected status, so no row in those tables prices a miss the case did not ask for.

## Commands

```bash
env GOPATH=/private/tmp/go-router-bench-gopath GOMODCACHE=/private/tmp/go-router-bench-gopath/pkg/mod GOCACHE=/private/tmp/go-router-bench-gocache go test -run '^$' -bench '^(BenchmarkRouters|BenchmarkParamAccess|BenchmarkFiveParams|BenchmarkScale)$' -benchmem -benchtime=1s -count=10 -timeout=60m .
```

Run from the repository root:

```bash
env GOCACHE=/private/tmp/go-router-docs-gocache go test -run '^$' -bench '^BenchmarkHost(Exact|Param|Any|Fallback)$' -benchmem -benchtime=1s -count=10 .
```

The first command runs from `benchmarks`. The temporary cache paths avoid host cache-permission differences and do not affect the benchmarked code.

## Router comparison

Nine routes, and a handler that reads no parameter.

| Case | Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| Static | go-router | 87.7 | 84.0–101.0 | 320 | 1 |
| Static | go-router pooled | 53.8 | 52.1–56.7 | 0 | 0 |
| Static | chi 5.3.2 | 127.3 | 125.6–129.2 | 368 | 2 |
| Static | echo 5.3.1 | 37.1 | 36.8–37.4 | 0 | 0 |
| Static | `http.ServeMux` | 96.7 | 96.2–98.1 | 0 | 0 |
| Parameter | go-router | 89.8 | 89.5–90.2 | 320 | 1 |
| Parameter | go-router pooled | 57.9 | 57.8–58.3 | 0 | 0 |
| Parameter | chi 5.3.2 | 219.1 | 218.0–219.8 | 704 | 4 |
| Parameter | echo 5.3.1 | 45.0 | 44.9–45.4 | 0 | 0 |
| Parameter | `http.ServeMux` | 98.0 | 97.4–99.5 | 16 | 1 |
| Deep | go-router | 118.7 | 118.0–120.3 | 320 | 1 |
| Deep | go-router pooled | 85.9 | 85.5–86.4 | 0 | 0 |
| Deep | chi 5.3.2 | 311.6 | 308.7–316.8 | 704 | 4 |
| Deep | echo 5.3.1 | 74.3 | 73.7–76.4 | 0 | 0 |
| Deep | `http.ServeMux` | 261.1 | 258.8–266.5 | 112 | 3 |

Every range is within 3% of its median, except the two go-router static rows: the unpooled one's worst sample landed 15% over and the pooled one's 6% over.

## Parameter access

The table above never reads a parameter, and that favours the two routers that defer the lookup: chi resolves a name in `URLParam` and `http.ServeMux` in `PathValue`, while go-router and Echo have already filled the context. These rows repeat the parameter and deep cases with a handler that asks for every name in the pattern.

| Case | Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| One parameter | go-router | 86.4 | 85.0–113.8 | 320 | 1 |
| One parameter | go-router pooled | 56.9 | 56.2–57.1 | 0 | 0 |
| One parameter | chi 5.3.2 | 199.9 | 198.1–201.7 | 704 | 4 |
| One parameter | echo 5.3.1 | 39.0 | 38.9–39.3 | 0 | 0 |
| One parameter | `http.ServeMux` | 101.2 | 100.5–104.5 | 16 | 1 |
| Three parameters | go-router | 130.5 | 127.8–132.4 | 320 | 1 |
| Three parameters | go-router pooled | 96.0 | 94.5–98.0 | 0 | 0 |
| Three parameters | chi 5.3.2 | 326.4 | 324.1–332.9 | 704 | 4 |
| Three parameters | echo 5.3.1 | 83.6 | 82.8–84.5 | 0 | 0 |
| Three parameters | `http.ServeMux` | 284.4 | 278.0–292.0 | 112 | 3 |

Three reads cost the pooled router 39 ns over one, Echo 45 ns, and `http.ServeMux` 183 ns. A `PathValue` call walks the matched pattern's segment list, so the stdlib's advantage in the first table is a debt the handler pays later. The one wide range is go-router's single-parameter case, whose worst sample landed 32% over its median; its other nine samples span 1 ns.

## Five parameters

One route, `/{a}/{b}/{c}/{d}/{e}`, with all five names read. Five crosses the four-slot inline parameter array, so the router spends one allocation on the overflow and the pooled row stops being allocation-free.

| Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| go-router | 167.9 | 165.6–173.8 | 448 | 2 |
| go-router pooled | 144.8 | 143.3–146.5 | 128 | 1 |
| chi 5.3.2 | 363.6 | 360.3–394.1 | 704 | 4 |
| echo 5.3.1 | 93.5 | 93.2–94.8 | 0 | 0 |
| `http.ServeMux` | 210.4 | 207.2–215.2 | 240 | 4 |

The overflow costs 128 bytes and one allocation, which is the documented price of a sixth parameter and the reason `InlineParamBudget` exists.

## Scale

184 routes over four methods, taken from the GitHub API. The parameter case matches `/repos/{owner}/{repo}/pulls/{number}/comments` and reads three names.

| Case | Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| Static | go-router | 90.4 | 89.1–91.8 | 320 | 1 |
| Static | go-router pooled | 55.0 | 54.8–55.9 | 0 | 0 |
| Static | chi 5.3.2 | 140.4 | 139.1–142.6 | 368 | 2 |
| Static | echo 5.3.1 | 40.0 | 39.8–40.4 | 0 | 0 |
| Static | `http.ServeMux` | 68.7 | 68.4–71.4 | 0 | 0 |
| Parameter | go-router | 140.9 | 140.7–145.9 | 320 | 1 |
| Parameter | go-router pooled | 107.2 | 106.3–108.6 | 0 | 0 |
| Parameter | chi 5.3.2 | 367.9 | 363.2–379.5 | 704 | 4 |
| Parameter | echo 5.3.1 | 103.5 | 102.2–104.4 | 0 | 0 |
| Parameter | `http.ServeMux` | 242.2 | 239.8–253.4 | 112 | 3 |
| Not found | go-router | 146.8 | 146.0–148.7 | 352 | 3 |
| Not found | go-router pooled | 111.0 | 110.0–113.8 | 32 | 2 |
| Not found | chi 5.3.2 | 224.4 | 222.5–243.3 | 416 | 5 |
| Not found | echo 5.3.1 | 633.5 | 541.8–650.5 | 424 | 8 |
| Not found | `http.ServeMux` | 724.0 | 624.5–777.9 | 320 | 18 |

Twenty times the route table moves the pooled router by about 1 ns on a static match and about 11 ns on a three-parameter match. The targets are not the same paths as in the tables above, so read this as a magnitude rather than a controlled delta: the trie walks the request, not the table, and a table this much larger does not change the shape of any column.

A miss reverses the ranking. Echo is 5.7x slower than the pooled router there, and `http.ServeMux` 6.5x at 18 allocations. Both build a response the routers ahead of them do not: a router that answers 404 quickly is worth more under a scanner than under a load test.

## Reading the tables

Echo leads every match, by 17 ns on the nine-route static case and 4 ns on the three-parameter match against 184 routes. Its lead is widest where the work is smallest and narrows as the path grows, which is the shape of a fixed per-request cost rather than a faster tree.

The pooled router is second everywhere and allocation-free in every case that stays inside the inline parameter array. `New` costs a flat 320 bytes and one allocation for the context, worth 23 to 36 ns; that gap is the constructor, not the tree, and it does not grow with the route table.

chi is last in every match case and never matches with fewer than two allocations. `http.ServeMux` is competitive on static routes, loses the ground back at `PathValue`, and is the slowest router in the table on a miss.

Do not read these tables against an older recording. Numbers from two sessions are not comparable, and a previous recording ran under transient scheduler load with ranges as wide as 97.1–592.5 ns.

## Pooling cost

Measured in an earlier session, between `65817fc` and `655139c`, the commit before this line of work started, with ten samples on each side. These deltas are not derived from the recording above and are kept for what they explain:

| Case | Unpooled | Pooled |
|---|---:|---:|
| Static | -3.2% | +12.3% |
| Parameter | -1.7% | +11.2% |
| Deep | +1.7% | +8.7% |

The pooled rows are slower on purpose, and the cost is two guarantees. About six points of it is `requestPath`, which now scans the path for a percent and a backslash so that `%5C` and `\` stay distinct routes; the old check read `URL.RawPath` and got that wrong. Another two to four points is the cleanup `release` runs before `sync.Pool.Put`, which drops the request, the `ResponseWriter` and the matched parameter strings so a context waiting in the pool holds none of them. Removing both puts the pooled rows within 2% of `655139c`, and removing both is not on offer.

A profile of the remaining gap to Echo is half trie walk and a sixth `sync.Pool`, with no single item left to remove.

Echo 5.3.1 lands where echo 4.15.4 did on the same machine. The major upgrade changed the API, not the speed.

## Host routing

| Case | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Exact | 98.0 | 97.0–99.9 | 320 | 1 |
| Parameter | 102.8 | 101.5–103.4 | 320 | 1 |
| Wildcard | 95.1 | 94.2–96.5 | 320 | 1 |
| Host-free fallback | 96.3 | 94.8–100.8 | 320 | 1 |

The host measurements include complete authority validation. The pooled measurements include clearing request and response references, alternate parameter storage, caches, deferred state, and per-request values before a context returns to the pool. Those checks intentionally trade a small amount of latency for bounded retention and cross-request isolation while keeping the pooled route path at zero allocations.

## Dependencies

The comparison uses chi 5.3.2 and Echo 5.3.1. Echo 5 makes `Context` a struct, so the benchmark handler takes `*echo.Context` rather than the v4 interface. The upgrade also collapsed the indirect tree: `gommon`, `go-colorable`, `go-isatty`, `fasttemplate`, `bytebufferpool`, `x/crypto`, `x/sys` and `x/text` are gone, leaving `x/net` 0.58.0 as the only indirect dependency.
