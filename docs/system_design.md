# Osimi Archive – Pipeline Orchestration Design

## Purpose

This document explains **how the archival processing pipeline is orchestrated**, with a focus on:
- conditional execution (OCR, transcripts, derivatives)
- filesystem-first truth
- recovery after database loss
- eliminating fragile worker-to-worker handoffs

This design intentionally separates **decision-making** from **execution**.

---

## Core Principle

> **Workers do not decide what comes next.  
> They only perform a single task and record the result.**

All decisions about *what should run* are made by a lightweight **Orchestrator (Planner)**.

---

## Sources of Truth

### 1. Filesystem (Authoritative)
The filesystem represents the canonical state of each object.

Evidence is derived from:
- presence of manifests (`meta/ingest.json`, `meta/catalog.json`)
- presence of output files (`ocr/page_0001.txt`, PDFs, transcripts)
- marker files (`OCR_DONE`, `TRANSCRIPT_DONE`, etc.)
- system-owned pipeline state (`meta/pipeline.json`)

This allows full recovery even if the database is deleted.

### 2. SQLite Database (Derived / Cache)
The database provides:
- fast querying
- job coordination
- lifecycle flags
- search indexing

It is **rebuildable** from filesystem state.

---

## Metadata-Driven Processing

### Batch-Level Knowledge

Each batch dropped into `ingest_drop/` contains the knowledge required to plan processing.

Example structure:

```
batch/
  page_0001.png
  page_0002.png
  catalog.json
  DONE
```

### `catalog.json` as Processing Intent

`catalog.json` is human-owned and may include an `item_kind` plus a `processing` block that declares **desired pipeline steps**.

Example:

```json
{
  "title": "Selected Works of Muhammad Osimi",
  "language": "eng",
  "item_kind": "scanned_document",
  "processing": {
    "ocr_text": {
      "enabled": true,
      "language": "eng"
    },
    "audio_transcript": {
      "enabled": false
    }
  }
}
```

Defaults (v1):
- OCR and transcript intent are inferred from `item_kind` if `processing` is absent.
- Language defaults to `classification.language` when present.

---

## Orchestrator (Planner)

### Responsibility

The Orchestrator:
- scans existing objects
- reads metadata (`meta/catalog.json`)
- inspects filesystem markers
- enqueues only the jobs that are **needed and missing**

It does **not** perform heavy work.

---

## Job Model

Jobs are **pull-based**.

Workers:
- poll the jobs table
- claim jobs atomically
- perform work
- write filesystem outputs
- write completion markers
- mark job succeeded or failed

Workers never enqueue follow-up jobs.

---

## Conditional Job Planning

### OCR Text Job

**Desired if:**
- `processing.ocr_text.enabled == true`

**Complete if:**
- `ocr/OCR_DONE` exists

**Action:**
- If desired and not complete → enqueue OCR job

---

### Audio Transcript Job (Future)

**Desired if:**
- `processing.audio_transcript.enabled == true`

**Complete if:**
- `audio/TRANSCRIPT_DONE` exists

**Action:**
- If desired and not complete → enqueue transcript job

---

### Derivatives (PDF, Thumbnails)

Derivatives are treated as **delivery artifacts**.

They may be:
- generated eagerly
- generated lazily on request

Presence of files determines availability; no DB flags required.

---

## Marker Files

Marker files are used to record durable completion.

Examples:
```
ocr/OCR_DONE
audio/TRANSCRIPT_DONE
```

Properties:
- cheap to check
- survive DB loss
- prevent duplicate work
- easy to reason about

---

## Pipeline State (System-Owned)

Pipeline state is recorded in `meta/pipeline.json` and may be used alongside marker files.

It tracks:
- which tasks ran
- status (`queued`, `running`, `done`, `failed`)
- parameters (language, engine, version)
- output references

This file is not authoritative and can be rebuilt from outputs + markers.

---

## Recovery After Database Loss

If SQLite is deleted:

1. Recreate schema
2. Scan `objects/`
3. For each object:
   - read `meta/ingest.json`
   - read `meta/catalog.json`
   - inspect marker files
4. Reinsert objects into DB
5. Reimport OCR text into `page_text`
6. Enqueue missing jobs based on metadata vs markers

No manual intervention required.

---

## Lifecycle States (High Level)

```
ingested
  ↓
ocr_running
  ↓
index_done
```

Derivative generation does not gate search readiness.

---

## Key Invariants

- Filesystem is the source of truth
- Workers never orchestrate
- Orchestrator is idempotent
- Jobs are safe to retry
- Outputs are regenerable unless explicitly marked otherwise

---

## Why This Design Scales

This architecture allows:
- adding new pipeline stages without refactoring workers
- conditional execution per object
- safe retries and crash recovery
- clean separation of concerns
- future expansion (ML OCR, better transcripts, new derivatives)

---

## Summary

The pipeline is **metadata-driven**, **filesystem-authoritative**, and **orchestrated centrally**.

This avoids fragile handoffs and ensures the archive remains correct, recoverable, and extensible over time.