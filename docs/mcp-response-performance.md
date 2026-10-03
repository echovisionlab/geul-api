# MCP accepted-response allocation measurements

The focused document mutation response now validates and builds its accepted
object, adds `block_id` when supplied, and serializes the object once. Previously
it serialized the accepted object, decoded it to add `block_id`, and serialized
it again. Document response encoders were also moved into
`internal/adapters/mcp/document_responses.go`.

`BenchmarkFocusedAccepted` measures `encodeFocusedAccepted` followed by
`structuredResult`: the complete text/structured response construction boundary,
without application execution or transport I/O. Its fixed fixtures contain one
or 32 accepted changes, with and without a created block. The same benchmark file
was copied into the unchanged baseline source at commit `615aa5b` and run against
the changes on `refactor/mcp-response-composition`.

## Conditions and results

Measured on 2026-10-03 with Go 1.26.8, darwin/arm64, Apple M2 Pro,
`GOWORK=off`, and `GOMAXPROCS=2`. Each fixture ran 1,000 iterations, repeated five
times. The table reports the median of each measurement across those repeats.

| Fixture | Before B/op | After B/op | Before allocs/op | After allocs/op |
| --- | ---: | ---: | ---: | ---: |
| 1 change, created block | 3,969 | 2,600 | 100 | 60 |
| 32 changes, created block | 34,227 | 20,506 | 1,115 | 614 |
| 1 change, no created block | 2,336 | 2,336 | 53 | 53 |
| 32 changes, no created block | 20,346 | 20,342 | 607 | 607 |

The created-block fixtures allocate 40% and 44.9% fewer objects and 34.5% and
40.1% fewer bytes, respectively. The control fixtures have unchanged allocation
counts. Their four-byte median difference in the 32-change case is pool/sample
variation and is not treated as an improvement.

CPU timings were recorded, but concurrent Next.js builds, analyzer work, and
system applications prevented a fair timing comparison. These raw `ns/op` samples
are retained for traceability; no latency improvement is claimed:

| Fixture | Before ns/op, five repeats | After ns/op, five repeats |
| --- | --- | --- |
| 1 change, created block | 7877, 7815, 9635, 8262, 6257 | 3218, 3143, 3339, 3054, 3136 |
| 32 changes, created block | 92350, 170131, 197012, 94518, 109819 | 30874, 30682, 30250, 30943, 34491 |
| 1 change, no created block | 3563, 2637, 2877, 2739, 2782 | 2892, 2705, 2660, 2830, 2711 |
| 32 changes, no created block | 47614, 46090, 44855, 38074, 51521 | 30964, 30588, 30592, 31388, 30428 |

The original local captures were `/tmp/dsub-mcp-response-before-root.txt` and
`/tmp/dsub-mcp-response-after-root.txt`; the results above remain usable without
those temporary files.

## Reproduction

From the changed branch checkout, export the baseline and copy only the identical
benchmark file into it. This works because the private encoder and response
helpers already exist in the baseline package.

```sh
review_root="$(git rev-parse --show-toplevel)"
baseline_dir="$(mktemp -d)"
log_dir="$(mktemp -d)"
git archive 615aa5b | tar -x -C "$baseline_dir"
cp "$review_root/internal/adapters/mcp/document_responses_benchmark_test.go" \
  "$baseline_dir/internal/adapters/mcp/document_responses_benchmark_test.go"
cmp "$review_root/internal/adapters/mcp/document_responses_benchmark_test.go" \
  "$baseline_dir/internal/adapters/mcp/document_responses_benchmark_test.go"
(cd "$baseline_dir" && GOWORK=off GOMAXPROCS=2 go test ./internal/adapters/mcp \
  -run '^$' -bench '^BenchmarkFocusedAccepted$' -benchmem -benchtime=1000x -count=5) \
  | tee "$log_dir/before.txt"
(cd "$review_root" && GOWORK=off GOMAXPROCS=2 go test ./internal/adapters/mcp \
  -run '^$' -bench '^BenchmarkFocusedAccepted$' -benchmem -benchtime=1000x -count=5) \
  | tee "$log_dir/after.txt"
```

Use the same toolchain and host settings for both runs and stop competing work
before using timing results. Compare each fixture separately.

## Behavior verification

Golden tests check the exact `dr`, `tr`, `c`, and `block_id` wire representation,
including omitted target revisions, null affected handles, empty change arrays,
and text/structured content and `IsError` parity. Invalid application results
still fail closed with the same errors before any response is exposed. The
authorization and aggregate CAS boundary and the `StructuredContent` map types
are unchanged.

The MCP adapter suite, its race tests, `go vet`, formatting checks, and the full
`go test ./...` suite passed. Allocation reductions plus these wire/error checks
provide the verified evidence; end-to-end request latency was not measured.
