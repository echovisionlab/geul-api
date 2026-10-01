# Public read consistency and query budgets

Measured on 2026-10-01 against baseline `6d4adff`, using the same isolated
integration fixtures and page sizes before and after each change. Fixture
writes were excluded. No production database was used.

| Public list | Rows | SQL before | SQL after | Reduction |
| --- | ---: | ---: | ---: | ---: |
| Work | 1 | 8 | 7 | 12.5% |
| Work | 6 | 13 | 7 | 46.2% |
| Work | 20 | 27 | 7 | 74.1% |
| Program Event | 1 | 11 | 11 | 0% |
| Program Event | 6 | 31 | 11 | 64.5% |
| Program Event | 20 | 86 | 11 | 87.2% |
| Program Event Type | 1 | 4 | 3 | 25.0% |
| Program Event Type | 6 | 14 | 3 | 78.6% |
| Program Event Type | 20 | 42 | 3 | 92.9% |

The Work fixture contains 20 published Works without featured images. GORM
Query and Row callbacks count SELECTs, including the redundant per-Work root
lookup removed by reusing `FeaturedImageFileID`. Ready-asset resolution for
Works with images remains per source file because the current media port has
no batch operation. This table does not measure that cost.

The Program Event fixture contains 20 published Events and 20 active types.
Each Event has a distinct type with de/en/fr locales; requested fr alternates
with source de fallback. Nineteen Events have ready posters, the first has a
non-primary candidate, and the final Event has no poster. GORM Trace counts
SQL statements. Poster selection, locale fallback, ordering, and optional
asset failures retain their existing behavior. Normal relation loading and
asset resolution use one batch per result page; failed optional batch reads
retain the prior per-item fallback.

Query counts establish that these relation reads no longer grow per returned
row under the measured conditions. Request latency and production RSS were
not measured; these deterministic counts isolate the actual removed queries
from network and database scheduling noise.

Run the query-budget regression tests through the cataloged integration runner:

```sh
GOWORK=off go run -tags=integration ./scripts/test/integration --package ./internal/work/public/integration --schema-root ../geul-schema
GOWORK=off go run -tags=integration ./scripts/test/integration --package ./internal/programevent/public --schema-root ../geul-schema
```

Work detail reads additionally snapshot Work-owned relations together with the
authorized root and document. The concurrency test holds the read at its
post-transaction asset projection, completes unpublish and draft relation
writes, and only then releases the response. It verifies the original
published document, image source, clients, credit groups, and credits remain
coherent. Public Member/Artist identity and asset readiness are resolved after
the transaction and retain their independent lifecycle.
