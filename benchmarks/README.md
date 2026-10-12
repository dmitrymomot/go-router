# Router benchmarks

These results were recorded on 2026-10-12 from `8b50596`, in one session, after the router indexed the fallbacks of its scopes, compiled a new scope alone, reused the route hit in dispatch, stopped matching a trailing slash against a route without one, and shrank `Base` so that an unpooled request allocates 288 bytes instead of 320.

| Environment | Value |
|---|---|
| Go | `go1.27.1 darwin/arm64` |
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
env GOCACHE=/private/tmp/go-router-docs-gocache go test -run '^$' -bench '^(BenchmarkHost(Exact|Param|Any|Fallback)|BenchmarkScopes1000NotFound|BenchmarkScopes1000NoScope|BenchmarkRegisterScopes1000)$' -benchmem -benchtime=1s -count=10 .
```

The first command runs from `benchmarks`. The temporary cache paths avoid host cache-permission differences and do not affect the benchmarked code.

The machine was not idle: other test runs shared it during the session. Most rows stay within 5% of their median, and the rows whose worst sample landed further out are named under each table. Read the medians, not the ranges.

## Router comparison

Nine routes, and a handler that reads no parameter.

| Case | Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| Static | go-router | 90.4 | 86.7–101.4 | 288 | 1 |
| Static | go-router pooled | 49.3 | 48.6–50.1 | 0 | 0 |
| Static | chi 5.3.2 | 126.9 | 123.3–133.4 | 368 | 2 |
| Static | echo 5.3.1 | 36.1 | 35.4–38.0 | 0 | 0 |
| Static | `http.ServeMux` | 100.3 | 98.6–101.6 | 0 | 0 |
| Parameter | go-router | 92.9 | 91.5–97.1 | 288 | 1 |
| Parameter | go-router pooled | 55.2 | 54.5–56.9 | 0 | 0 |
| Parameter | chi 5.3.2 | 215.6 | 213.7–219.3 | 704 | 4 |
| Parameter | echo 5.3.1 | 44.4 | 43.8–45.5 | 0 | 0 |
| Parameter | `http.ServeMux` | 104.9 | 103.2–106.2 | 16 | 1 |
| Deep | go-router | 119.6 | 118.7–142.5 | 288 | 1 |
| Deep | go-router pooled | 83.9 | 82.5–96.0 | 0 | 0 |
| Deep | chi 5.3.2 | 316.0 | 308.7–567.3 | 704 | 4 |
| Deep | echo 5.3.1 | 72.7 | 71.2–84.0 | 0 | 0 |
| Deep | `http.ServeMux` | 280.6 | 273.8–308.5 | 112 | 3 |

The deep rows and the unpooled static row each had one or two slow samples, between 10% and 19% over the median, and chi's deep row one at 80% over; the other samples of each sit within 3%.

## Parameter access

The table above never reads a parameter, and that favours the two routers that defer the lookup: chi resolves a name in `URLParam` and `http.ServeMux` in `PathValue`, while go-router and Echo have already filled the context. These rows repeat the parameter and deep cases with a handler that asks for every name in the pattern.

| Case | Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| One parameter | go-router | 92.7 | 89.9–156.9 | 288 | 1 |
| One parameter | go-router pooled | 55.0 | 54.1–82.0 | 0 | 0 |
| One parameter | chi 5.3.2 | 216.3 | 205.7–366.4 | 704 | 4 |
| One parameter | echo 5.3.1 | 38.7 | 37.7–43.7 | 0 | 0 |
| One parameter | `http.ServeMux` | 114.2 | 106.2–119.2 | 16 | 1 |
| Three parameters | go-router | 139.3 | 131.3–182.8 | 288 | 1 |
| Three parameters | go-router pooled | 105.8 | 99.0–115.3 | 0 | 0 |
| Three parameters | chi 5.3.2 | 349.1 | 325.8–504.6 | 704 | 4 |
| Three parameters | echo 5.3.1 | 82.7 | 80.8–85.5 | 0 | 0 |
| Three parameters | `http.ServeMux` | 298.3 | 285.8–301.7 | 112 | 3 |

Three reads cost the pooled router 51 ns over one, Echo 44 ns, and `http.ServeMux` 184 ns. A `PathValue` call walks the matched pattern's segment list, so the stdlib's advantage in the first table is a debt the handler pays later. This table took the worst of the shared machine: the go-router and chi rows each hold a sample 30% to 69% over the median.

## Five parameters

One route, `/{a}/{b}/{c}/{d}/{e}`, with all five names read. Five crosses the four-slot inline parameter array, so the router spends one allocation on the overflow and the pooled row stops being allocation-free.

| Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| go-router | 167.8 | 164.8–172.5 | 416 | 2 |
| go-router pooled | 138.3 | 134.6–139.5 | 128 | 1 |
| chi 5.3.2 | 366.7 | 360.0–445.0 | 704 | 4 |
| echo 5.3.1 | 91.0 | 90.4–91.5 | 0 | 0 |
| `http.ServeMux` | 212.0 | 204.4–217.4 | 240 | 4 |

The overflow costs 128 bytes and one allocation, which is the documented price of a fifth parameter and the reason the router keeps four parameters inline.

## Scale

184 routes over four methods, taken from the GitHub API. The parameter case matches `/repos/{owner}/{repo}/pulls/{number}/comments` and reads three names.

| Case | Router | Median ns/op | Range ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| Static | go-router | 91.8 | 90.1–93.9 | 288 | 1 |
| Static | go-router pooled | 53.1 | 52.7–54.1 | 0 | 0 |
| Static | chi 5.3.2 | 140.4 | 135.6–145.4 | 368 | 2 |
| Static | echo 5.3.1 | 39.5 | 39.0–39.9 | 0 | 0 |
| Static | `http.ServeMux` | 69.4 | 68.9–71.9 | 0 | 0 |
| Parameter | go-router | 144.8 | 141.0–149.3 | 288 | 1 |
| Parameter | go-router pooled | 105.3 | 104.4–106.3 | 0 | 0 |
| Parameter | chi 5.3.2 | 377.2 | 366.1–392.1 | 704 | 4 |
| Parameter | echo 5.3.1 | 101.3 | 100.2–102.5 | 0 | 0 |
| Parameter | `http.ServeMux` | 238.2 | 237.2–242.7 | 112 | 3 |
| Not found | go-router | 167.1 | 165.9–171.1 | 304 | 2 |
| Not found | go-router pooled | 129.8 | 129.7–131.5 | 16 | 1 |
| Not found | chi 5.3.2 | 222.9 | 221.7–225.1 | 416 | 5 |
| Not found | echo 5.3.1 | 532.9 | 528.1–540.0 | 424 | 8 |
| Not found | `http.ServeMux` | 617.1 | 610.0–620.9 | 320 | 18 |

Twenty times the route table moves the pooled router by about 4 ns on a static match and by nothing measurable on a three-parameter match. The targets are not the same paths as in the tables above, so read this as a magnitude rather than a controlled delta: the trie walks the request, not the table, and a table this much larger does not change the shape of any column.

A miss reverses the ranking. Echo is 4.1x slower than the pooled router there, and `http.ServeMux` 4.8x at 18 allocations. Both build a response the routers ahead of them do not: a router that answers 404 quickly is worth more under a scanner than under a load test. The pooled miss now costs one allocation where it cost two. Its median sits about 19 ns above the 111 ns of the previous recording, and that gap is the price of `ResetForError`, which the text error handler now runs before it writes: it drops six headers a failed handler may have left. The harness reuses one header map across iterations, so each 404 deletes six keys from a map that still holds the previous answer's Content-Type and nosniff, and a delete hashes its key even when the key is missing. net/http hands every request a fresh map, where the reset skips the deletes: with the map cleared before each request, the pooled miss takes 104.5 ns against 112.5 ns for `d81bbd9`. The two go-router rows were rerun in a later session, after the reset stopped canonicalizing "ETag" on every call and learned to skip an empty header; on `8b50596` the pooled miss measured 160.9 ns and the unpooled one 197.4 ns.

## Reading the tables

Echo leads every match, by 13 ns on the nine-route static case and 4 ns on the three-parameter match against 184 routes. Its lead is widest where the work is smallest and narrows as the path grows, which is the shape of a fixed per-request cost rather than a faster tree.

The pooled router is second everywhere and allocation-free in every case that stays inside the inline parameter array. `New` costs a flat 288 bytes and one allocation for the context, worth 36 to 41 ns; that gap is the constructor, not the tree, and it does not grow with the route table.

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
| Exact | 107.2 | 98.0–146.3 | 288 | 1 |
| Parameter | 112.5 | 109.8–122.4 | 288 | 1 |
| Wildcard | 96.6 | 94.9–102.7 | 288 | 1 |
| Host-free fallback | 97.9 | 97.6–103.0 | 288 | 1 |

The host measurements include complete authority validation. The pooled measurements include clearing request and response references, alternate parameter storage, caches, deferred state, and per-request values before a context returns to the pool. Those checks intentionally trade a small amount of latency for bounded retention and cross-request isolation while keeping the pooled route path at zero allocations. The exact row holds one sample 36% over its median, and its lowest sample, 98.0 ns, is closer to the other rows.

## Many scopes

1000 scopes opened with `Route("/r{i}/{id}", ...)`, each with one route, on an unpooled router. These root benchmarks price the scope index: a 404 consults it to find the scope whose middleware and error handler answer.

| Case | Median | Range | B/op | allocs/op |
|---|---:|---:|---:|---:|
| 404 under a scope, `/r500/7/missing` | 261.2 ns | 259.2–262.1 ns | 304 | 2 |
| 404 under no scope, `/nothing/here` | 193.7 ns | 191.6–195.2 ns | 304 | 2 |
| Registering the 1000 scopes | 5.52 ms | 5.43–5.57 ms | 4.9 MiB | 66397 |

Before the index, the fallback tested every prefixed scope in turn and registration compiled the whole graph on every `Route`: in the session that measured the change, the first 404 took 5172 ns, the second 9890 ns, and registering took 252 ms and 489 MiB. Those older numbers come from that session, not this one. The two 404 rows were rerun with the not-found rows above, after the `ResetForError` fix; on `8b50596` they measured 296.6 ns and 221.1 ns.

## Dependencies

The comparison uses chi 5.3.2 and Echo 5.3.1. Echo 5 makes `Context` a struct, so the benchmark handler takes `*echo.Context` rather than the v4 interface. The upgrade also collapsed the indirect tree: `gommon`, `go-colorable`, `go-isatty`, `fasttemplate`, `bytebufferpool`, `x/crypto`, `x/sys` and `x/text` are gone, leaving `x/net` 0.58.0 as the only indirect dependency.
