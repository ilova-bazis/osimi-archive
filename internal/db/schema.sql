PRAGMA foreign_keys = ON;


CREATE TABLE IF NOT EXISTS counters (date_key TEXT PRIMARY KEY, last_value INTEGER NOT NULL);


CREATE TABLE IF NOT EXISTS objects (
    object_id TEXT PRIMARY KEY,
    object_root TEXT NOT NULL,
    year INTEGER NOT NULL,
    month INTEGER NOT NULL CHECK (month BETWEEN 1 AND 12),
    processing_state TEXT NOT NULL CHECK (
        processing_state IN (
            'queued',
            'ingesting',
            'ingested',
            'derivatives_running',
            'derivatives_done',
            'ocr_running',
            'ocr_done',
            'index_running',
            'index_done',
            'processing_failed',
            'processing_skipped'
        )
    ),
    curation_state TEXT NOT NULL CHECK (
        curation_state IN ('needs_review', 'review_in_progress', 'reviewed', 'curation_failed')
    ),
    ingest_manifest_rel TEXT NOT NULL DEFAULT 'meta/ingest.json',
    catalog_manifest_rel TEXT NOT NULL DEFAULT 'meta/catalog.json',
    page_count INTEGER,
    has_access_pdf INTEGER NOT NULL DEFAULT 0 CHECK (has_access_pdf IN (0, 1)),
    has_ocr INTEGER NOT NULL DEFAULT 0 CHECK (has_ocr IN (0, 1)),
    has_index INTEGER NOT NULL DEFAULT 0 CHECK (has_index IN (0, 1)),
    last_error TEXT,
    last_error_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);


CREATE INDEX IF NOT EXISTS idx_objects_processing_state ON objects (processing_state);


CREATE INDEX IF NOT EXISTS idx_objects_curation_state ON objects (curation_state);


CREATE INDEX IF NOT EXISTS idx_objects_year_month ON objects (year, month);


CREATE TABLE IF NOT EXISTS jobs (
    job_id text PRIMARY KEY,
    object_id text NOT NULL,
    job_type text NOT NULL CHECK (job_type IN ('ingest', 'derivatives', 'ocr', 'index')),
    state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    attempt integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 3,
    queued_at text NOT NULL,
    started_at text,
    finished_at text,
    error_message text,
    payload_json text,
    FOREIGN KEY (object_id) REFERENCES objects (object_id) ON DELETE CASCADE
);


CREATE INDEX IF NOT EXISTS idx_jobs_object_id ON jobs (object_id);


CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs (state);


CREATE INDEX IF NOT EXISTS idx_jobs_type_state ON jobs (job_type, state);


CREATE TABLE IF NOT EXISTS job_events (
    event_id integer PRIMARY KEY AUTOINCREMENT,
    job_id text NOT NULL,
    object_id TEXT NOT NULL,
    level TEXT NOT NULL CHECK (level IN ('debug', 'info', 'warn', 'error')),
    message TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY (job_id) REFERENCES jobs (job_id) ON DELETE CASCADE,
    FOREIGN KEY (object_id) REFERENCES objects (object_id) ON DELETE CASCADE
);


CREATE INDEX IF NOT EXISTS idx_job_events_job_id ON job_events (job_id);


CREATE INDEX IF NOT EXISTS idx_job_events_object_id ON job_events (object_id);


CREATE TABLE IF NOT EXISTS catalog_cache (
    object_id TEXT PRIMARY KEY,
    access_level TEXT CHECK (access_level IN ('private', 'family', 'public')),
    title_primary TEXT,
    doc_type TEXT,
    language TEXT, -- tg/fa/ru/en
    tags_csv TEXT, -- comma-separated canonical tags
    published_value TEXT, -- "YYYY" or "YYYY-MM" or "YYYY-MM-DD" or NULL
    published_approx INTEGER CHECK (published_approx IN (0, 1)),
    summary TEXT,
    updated_at TEXT NOT NULL,
    source_catalog_mtime TEXT,
    FOREIGN KEY (object_id) REFERENCES objects (object_id) ON DELETE CASCADE
);


CREATE INDEX IF NOT EXISTS idx_catalog_cache_access_level ON catalog_cache (access_level);


CREATE INDEX IF NOT EXISTS idx_catalog_cache_doc_type ON catalog_cache (doc_type);


CREATE INDEX IF NOT EXISTS idx_catalog_cache_language ON catalog_cache (language);


CREATE TABLE IF NOT EXISTS page_text (
    object_id TEXT NOT NULL,
    page_number INTEGER NOT NULL,
    ocr_version TEXT NOT NULL DEFAULT 'v1',
    text TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (object_id, page_number, ocr_version),
    FOREIGN KEY (object_id) REFERENCES objects (object_id) ON DELETE CASCADE
);


CREATE INDEX IF NOT EXISTS idx_page_text_object_id ON page_text (object_id);


CREATE VIRTUAL TABLE IF NOT EXISTS page_text_fts USING fts5 (
    object_id,
    page_number UNINDEXED,
    ocr_version UNINDEXED,
    text,
    content = 'page_text',
    content_rowid = 'rowid'
);


CREATE TRIGGER IF NOT EXISTS page_text_ai AFTER INSERT ON page_text BEGIN
INSERT INTO
    page_text_fts (rowid, object_id, page_number, ocr_version, text)
VALUES
    (new.rowid, new.object_id, new.page_number, new.ocr_version, new.text);


END;


CREATE TRIGGER IF NOT EXISTS page_text_ad AFTER DELETE ON page_text BEGIN
INSERT INTO
    page_text_fts (page_text_fts, rowid, object_id, page_number, ocr_version, text)
VALUES
    ('delete', old.rowid, old.object_id, old.page_number, old.ocr_version, old.text);


END;


CREATE TRIGGER IF NOT EXISTS page_text_au AFTER
UPDATE ON page_text BEGIN
INSERT INTO
    page_text_fts (page_text_fts, rowid, object_id, page_number, ocr_version, text)
VALUES
    ('delete', old.rowid, old.object_id, old.page_number, old.ocr_version, old.text);


INSERT INTO
    page_text_fts (rowid, object_id, page_number, ocr_version, text)
VALUES
    (new.rowid, new.object_id, new.page_number, new.ocr_version, new.text);


END;


CREATE TABLE IF NOT EXISTS batch_ingests (
    batch_path TEXT PRIMARY KEY,
    object_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY (object_id) REFERENCES objects (object_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS orchestrator_wakeup (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    last_signal_at TEXT NOT NULL
);
