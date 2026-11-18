\set ON_ERROR_STOP on
\pset pager off
-- This script owns a fresh schema and is restricted to a dedicated analysis DB.
SELECT current_database() ~ '^labreserve_analysis(_[a-zA-Z0-9_]+)?$' AS safe_database \gset
\if :safe_database
\else
  \echo 'Refusing analysis: use a database named labreserve_analysis or labreserve_analysis_*'
  \quit 3
\endif

SELECT 'explain_' || pg_backend_pid() AS analysis_schema \gset
CREATE SCHEMA :"analysis_schema";
SET search_path TO :"analysis_schema";

SELECT version();
SHOW shared_buffers;
SHOW work_mem;

CREATE TABLE devices (
    id text PRIMARY KEY,
    name text NOT NULL,
    kind text NOT NULL
);
CREATE TABLE reservations (
    id uuid PRIMARY KEY,
    device_id text NOT NULL REFERENCES devices(id),
    owner text NOT NULL,
    created_at timestamptz NOT NULL,
    released_at timestamptz
);
CREATE UNIQUE INDEX one_active_reservation_per_device
    ON reservations(device_id) WHERE released_at IS NULL;

INSERT INTO devices
SELECT 'analysis-' || n, 'Synthetic device ' || n, 'synthetic'
FROM generate_series(1, 100) AS n;

-- 200,000 historical rows across 100 devices, 2,000 per device.
-- MD5 is used only to construct reproducible synthetic UUIDs, not for security.
INSERT INTO reservations (id, device_id, owner, created_at, released_at)
SELECT md5(n::text)::uuid,
       'analysis-' || ((n % 100) + 1),
       'synthetic-engineer',
       timestamptz '2026-01-01 00:00:00+00' + n * interval '1 second',
       timestamptz '2026-01-01 00:00:00+00' + n * interval '1 second' + interval '30 seconds'
FROM generate_series(1, 200000) AS n;

ANALYZE reservations;
SELECT count(*) AS total_rows,
       count(*) FILTER (WHERE device_id = 'analysis-42') AS matching_device_rows
FROM reservations;

-- Exact projection and ordering used by Store.History, with concrete parameters.
\echo 'WITHOUT history index'
EXPLAIN (ANALYZE, BUFFERS)
SELECT id::text, device_id, owner, created_at, released_at
FROM reservations
WHERE device_id = 'analysis-42'
ORDER BY created_at DESC, reservations.id DESC
LIMIT 20 OFFSET 0;

CREATE INDEX reservations_device_history_idx
    ON reservations(device_id, created_at DESC, id DESC);
ANALYZE reservations;

\echo 'WITH history index'
EXPLAIN (ANALYZE, BUFFERS)
SELECT id::text, device_id, owner, created_at, released_at
FROM reservations
WHERE device_id = 'analysis-42'
ORDER BY created_at DESC, reservations.id DESC
LIMIT 20 OFFSET 0;

-- Deep OFFSET still scans and discards matching index entries.
\echo 'WITH history index, OFFSET 1000'
EXPLAIN (ANALYZE, BUFFERS)
SELECT id::text, device_id, owner, created_at, released_at
FROM reservations
WHERE device_id = 'analysis-42'
ORDER BY created_at DESC, reservations.id DESC
LIMIT 20 OFFSET 1000;

DROP SCHEMA :"analysis_schema" CASCADE;
