# Reservation history query experiment

Run only in a separate, disposable PostgreSQL database named
`labreserve_analysis` or `labreserve_analysis_*`. The script rejects any other
database before creating objects. It creates its own new `explain_<backend pid>`
schema, populates it with synthetic records, and removes only that schema on
success. If interrupted, locate that schema and remove it manually after checking
its name. The script never drops an existing application index or table.

```sh
createdb labreserve_analysis
psql "$ANALYSIS_DATABASE_URL" -X -f db/analysis/history.sql \
  > db/analysis/results.txt 2>&1
```

For example, set `ANALYSIS_DATABASE_URL` to an explicitly local connection for
this dedicated database. Do not use `DATABASE_URL` for the application database.
The role must be able to create schemas in the analysis database. Run from the
repository root and review the output for errors.

The dataset contains 200,000 completed reservations across 100 devices (2,000
reservations per device). Timestamps and UUIDs are reproducible. The projection,
predicate, ordering, limit and offset match the storage adapter's history query.
`ANALYZE` updates statistics before each plan. The first query runs before the
history index exists; the second runs after creating the same composite B-tree
index as the application migration. The partial active-reservation index exists
in both cases, but completed records cannot use it. A third query demonstrates
the work associated with a larger offset.

Compare scan type, sort, actual rows, execution time and shared buffers. The
composite index can select the device and supply the descending order directly,
so a small first page generally reads less data. This is a local educational
experiment, not a benchmark: cache warmth, hardware and PostgreSQL planner
settings affect timings. OFFSET pagination still scans/discards preceding rows;
keyset pagination is a possible future improvement if deep history becomes a
real requirement.

Actual measurements from the 2026-11-18 local run are captured in `results.txt`.
The database was PostgreSQL 16.15 on Linux x86-64 with `shared_buffers=128MB` and
`work_mem=4MB`. Other Go/Docker builds were running concurrently.

| Query | Actual plan | Execution time | Shared buffers |
|---|---|---:|---|
| First page without history index | Parallel sequential scan, sort, gather merge | 251.027 ms | 2,338 hits |
| First page with history index | Index scan, no sort | 0.167 ms | 20 hits, 3 reads |
| OFFSET 1000 with history index | Bitmap index/heap scan and sort | 17.911 ms | 2,003 hits, 14 reads |

This single sample verifies that the composite index can serve the actual
first-page query and that the planner can choose a different plan for a deeper
offset. Concurrent build load and cache differences mean these numbers cannot
establish a reliable speedup factor. Repeat the experiment under controlled
conditions before using timings for capacity or performance claims.
