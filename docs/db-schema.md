# Database Schema (v1) — SQLite

This document defines the **v1 SQLite schema** for the Osimi Archive.

The database is a **control plane** for:
- Tracking objects (`OBJ-...`) and their states (processing + curation)
- Tracking jobs/steps, retries, and errors
- Supporting the v1 UI queues (“Needs Review”, “Failures”, etc.)
- Powering search (optional v1 via SQLite FTS5)

> Source of truth for content remains: `original/` + `meta/ingest.json` + `meta/catalog.json`.
> The DB accelerates operations and UI, but the archive remains rebuildable from disk.

---

## Design principles

1. **Two-track states** are stored explicitly:
   - `processing_state`
   - `curation_state`
2. **Idempotent jobs**: jobs can be retried safely.
3. **Disk-first**: DB stores *references* to on-disk artifacts, not the artifacts themselves.
4. **Auditability**: every failure is recorded with a message + timestamp.
5. **Search-ready**: optional FTS5 tables can be enabled immediately or later.

---

## Entities (v1)

- **objects**: one row per archived item (`OBJ-...`)
- **jobs**: asynchronous units of work (ingest, derivatives, ocr, index)
- **job_events**: append-only log lines for visibility (optional but very useful)
- **catalog_cache**: parsed/minimal catalog fields for fast UI filtering (optional but recommended)
- **page_text_fts**: full-text search over OCR text (optional v1)

---

## Enumerations (v1)

### Processing states
`queued`, `ingesting`, `ingested`, `derivatives_running`, `derivatives_done`, `ocr_running`, `ocr_done`,
`index_running`, `index_done`, `processing_failed`, `processing_skipped`

### Curation states
`needs_review`, `review_in_progress`, `reviewed`, `curation_failed`

### Job types
`ingest`, `derivatives`, `ocr`, `index`

### Job states
`queued`, `running`, `succeeded`, `failed`, `cancelled`

---

## Schema SQL (v1)

> Notes:
> - Uses SQLite `TEXT` for IDs.
> - Uses UTC RFC3339 timestamps in `TEXT`.
> - Enforces basic constraints with `CHECK`.
> - Adds indexes for common UI queries.

```sql
PRAGMA foreign_keys = ON;

-- ============================================================
-- objects: one row per archived item (OBJ-...)
-- ============================================================
CREATE TABLE IF NOT EXISTS objects (
  object_id           TEXT PRIMARY KEY,                -- "OBJ-YYYYMMDD-XXXXXX"
  object_root         TEXT NOT NULL,                   -- absolute path to object folder (machine-specific)
  year                INTEGER NOT NULL,
  month               INTEGER NOT NULL CHECK (month BETWEEN 1 AND 12),

  -- Processing + curation state (see docs/lifecycle-states.md)
  processing_state    TEXT NOT NULL CHECK (
    processing_state IN (
      'queued','ingesting','ingested',
      'derivatives_running','derivatives_done',
      'ocr_running','ocr_done',
      'index_running','index_done',
      'processing_failed','processing_skipped'
    )
  ),
  curation_state      TEXT NOT NULL CHECK (
    curation_state IN (
      'needs_review','review_in_progress','reviewed','curation_failed'
    )
  ),

  -- Files on disk (relative to object_root)
  ingest_manifest_rel TEXT NOT NULL DEFAULT 'meta/ingest.json',
  catalog_manifest_rel TEXT NOT NULL DEFAULT 'meta/catalog.json',

  -- Derived info (optional convenience)
  page_count          INTEGER,
  has_access_pdf      INTEGER NOT NULL DEFAULT 0 CHECK (has_access_pdf IN (0,1)),
  has_ocr             INTEGER NOT NULL DEFAULT 0 CHECK (has_ocr IN (0,1)),
  has_index           INTEGER NOT NULL DEFAULT 0 CHECK (has_index IN (0,1)),

  -- Error tracking
  last_error          TEXT,
  last_error_at       TEXT,

  -- Audit timestamps
  created_at          TEXT NOT NULL,                   -- when object row created
  updated_at          TEXT NOT NULL                    -- last update
);

CREATE INDEX IF NOT EXISTS idx_objects_processing_state ON objects(processing_state);
CREATE INDEX IF NOT EXISTS idx_objects_curation_state ON objects(curation_state);
CREATE INDEX IF NOT EXISTS idx_objects_year_month ON objects(year, month);

-- ============================================================
-- jobs: async work units
-- ============================================================
CREATE TABLE IF NOT EXISTS jobs (
  job_id              TEXT PRIMARY KEY,                -- e.g. "JOB-..."; can be UUID
  object_id           TEXT NOT NULL,
  job_type            TEXT NOT NULL CHECK (job_type IN ('ingest','derivatives','ocr','index')),
  state               TEXT NOT NULL CHECK (state IN ('queued','running','succeeded','failed','cancelled')),

  -- Retry tracking
  attempt             INTEGER NOT NULL DEFAULT 0,
  max_attempts        INTEGER NOT NULL DEFAULT 3,

  -- Timing
  queued_at           TEXT NOT NULL,
  started_at          TEXT,
  finished_at         TEXT,

  -- Failure details (if any)
  error_message       TEXT,

  -- Optional payload (JSON string) for parameters, e.g. ocr languages
  payload_json        TEXT,

  FOREIGN KEY (object_id) REFERENCES objects(object_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_jobs_object_id ON jobs(object_id);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);
CREATE INDEX IF NOT EXISTS idx_jobs_type_state ON jobs(job_type, state);

-- ============================================================
-- job_events: append-only log for observability
-- ============================================================
CREATE TABLE IF NOT EXISTS job_events (
  event_id            INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id              TEXT NOT NULL,
  object_id           TEXT NOT NULL,
  level               TEXT NOT NULL CHECK (level IN ('debug','info','warn','error')),
  message             TEXT NOT NULL,
  created_at          TEXT NOT NULL,

  FOREIGN KEY (job_id) REFERENCES jobs(job_id) ON DELETE CASCADE,
  FOREIGN KEY (object_id) REFERENCES objects(object_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_job_events_job_id ON job_events(job_id);
CREATE INDEX IF NOT EXISTS idx_job_events_object_id ON job_events(object_id);

-- ============================================================
-- catalog_cache: fast UI filtering without parsing JSON every time
-- (optional but recommended)
-- ============================================================
CREATE TABLE IF NOT EXISTS catalog_cache (
  object_id           TEXT PRIMARY KEY,

  access_level        TEXT CHECK (access_level IN ('private','family','public')),
  title_primary       TEXT,
  doc_type            TEXT,                            -- mirrors classification.type
  language            TEXT,                            -- tg/fa/ru/en
  tags_csv            TEXT,                            -- comma-separated canonical tags (v1)
  published_value     TEXT,                            -- "YYYY" or "YYYY-MM" or "YYYY-MM-DD" or NULL
  published_approx    INTEGER CHECK (published_approx IN (0,1)),
  summary             TEXT,

  updated_at          TEXT NOT NULL,                   -- last time cache refreshed
  source_catalog_mtime TEXT,                           -- last known mtime of meta/catalog.json (optional)

  FOREIGN KEY (object_id) REFERENCES objects(object_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_catalog_cache_access_level ON catalog_cache(access_level);
CREATE INDEX IF NOT EXISTS idx_catalog_cache_doc_type ON catalog_cache(doc_type);
CREATE INDEX IF NOT EXISTS idx_catalog_cache_language ON catalog_cache(language);

-- ============================================================
-- Optional: OCR text table + FTS5 for full-text search
-- ============================================================

-- Stores per-page OCR text (authoritative enough for search, not for editing)
CREATE TABLE IF NOT EXISTS page_text (
  object_id           TEXT NOT NULL,
  page_number         INTEGER NOT NULL,
  ocr_version         TEXT NOT NULL DEFAULT 'v1',
  text                TEXT NOT NULL,
  updated_at          TEXT NOT NULL,

  PRIMARY KEY (object_id, page_number, ocr_version),
  FOREIGN KEY (object_id) REFERENCES objects(object_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_page_text_object_id ON page_text(object_id);

-- FTS virtual table for fast search. Requires SQLite compiled with FTS5.
-- This uses an external content table pattern so we can keep structured metadata in page_text.
CREATE VIRTUAL TABLE IF NOT EXISTS page_text_fts USING fts5(
  object_id,
  page_number UNINDEXED,
  ocr_version UNINDEXED,
  text,
  content='page_text',
  content_rowid='rowid'
);

-- Triggers to keep FTS in sync with page_text
CREATE TRIGGER IF NOT EXISTS page_text_ai AFTER INSERT ON page_text BEGIN
  INSERT INTO page_text_fts(rowid, object_id, page_number, ocr_version, text)
  VALUES (new.rowid, new.object_id, new.page_number, new.ocr_version, new.text);
END;

CREATE TRIGGER IF NOT EXISTS page_text_ad AFTER DELETE ON page_text BEGIN
  INSERT INTO page_text_fts(page_text_fts, rowid, object_id, page_number, ocr_version, text)
  VALUES ('delete', old.rowid, old.object_id, old.page_number, old.ocr_version, old.text);
END;

CREATE TRIGGER IF NOT EXISTS page_text_au AFTER UPDATE ON page_text BEGIN
  INSERT INTO page_text_fts(page_text_fts, rowid, object_id, page_number, ocr_version, text)
  VALUES ('delete', old.rowid, old.object_id, old.page_number, old.ocr_version, old.text);

  INSERT INTO page_text_fts(rowid, object_id, page_number, ocr_version, text)
  VALUES (new.rowid, new.object_id, new.page_number, new.ocr_version, new.text);
END;
```

---

## How the pipeline uses this DB (v1)

### On batch ready
1. Create `objects` row:
   - `processing_state = queued`
   - `curation_state = needs_review`
2. Create `jobs` row: `job_type=ingest`, `state=queued`

### On ingest start
- Set `processing_state = ingesting`
- Set ingest job `state=running`

### On ingest success
- Set `processing_state = ingested`
- Populate:
  - `page_count`
- Enqueue derivatives job

### On derivatives success
- Set `processing_state = derivatives_done`
- Set `has_access_pdf = 1`
- Enqueue OCR job (optional)

### On OCR success
- Set `processing_state = ocr_done`
- Set `has_ocr = 1`
- Populate `page_text` (per-page) and sync `page_text_fts`
- Enqueue index job (optional if you later move to OpenSearch)

### On index success
- Set `processing_state = index_done`
- Set `has_index = 1`

### On catalog save (Method 1)
- Validate `meta/catalog.json`
- If valid:
  - set `curation_state = reviewed`
  - refresh `catalog_cache`
- If invalid:
  - set `curation_state = curation_failed` and store error

---

## Recommended “review complete” validation

Use the rules from `docs/lifecycle-states.md` and `docs/catalog.json.md`.
At minimum:
- non-empty `title.primary`
- valid `access.level`
- valid `classification.type`
- valid `classification.language`
- tags unique

---

## Future expansions (non-breaking)

This schema intentionally leaves room for:
- `items` table (bibliographic record separate from physical objects)
- `entities` and `entity_mentions` for extracted people/places/orgs
- Multi-user accounts and permissions
- External index (OpenSearch) while retaining SQLite as control plane

---

## Status

This document defines **DB Schema v1** for SQLite.

Any breaking changes must be documented as a new version.