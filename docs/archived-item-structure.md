# Osimi Archive – Archived Item Folder Structure (v1)

This document defines the **canonical on-disk folder structure** for a single archived item
after ingestion into the Osimi Digital Archive.

This structure is designed to be:
- Human-browsable
- Machine-stable
- Archivally sound (immutability of originals)
- Fully rebuildable (derivatives, OCR, index)
- Suitable for long-term family and public preservation

---

## Root Archive Layout

```
/osimi-archive/
  /objects/     # Canonical archived items (source of truth)
  /cache/       # Temporary generated downloads (safe to delete)
  /exports/     # Curated exports intentionally created
  /logs/        # System and ingest logs
```

Only the `objects/` directory is considered **archival truth**.

---

## Objects Directory Layout

Archived items are partitioned by year and month to keep filesystem directories manageable.

```
/osimi-archive/objects/
  /YYYY/
    /MM/
      /OBJ-YYYYMMDD-XXXXXX/
```

### Example

```
/osimi-archive/objects/2026/01/OBJ-20260109-000123/
```

Each `OBJ-...` directory represents **exactly one logical document**.

---

## Archived Item Structure

```
OBJ-YYYYMMDD-XXXXXX/
  meta/
  original/
  derivatives/
  ocr/
  checksums/
  events/
```

### Invariants
1. Each archived item has exactly one `OBJ-...` directory.
2. `original/` contents are immutable after successful ingest.
3. `derivatives/` and `ocr/` contents are replaceable and versioned.
4. `meta/ingest.json` must exist for every archived item.
5. Page numbering is contiguous and starts at `page_0001`.

---

## meta/

Metadata and manifest files describing the archived item.

```
meta/
  ingest.json
  catalog.json   # optional (DB is canonical for catalog data)
```

### ingest.json (required)
Created automatically during ingest. Never manually edited.

Contains:
- `object_id`
- ingest timestamp
- ingest source path
- page count
- original filenames (provenance)
- tool and version metadata

### catalog.json (optional)
Snapshot of human-entered catalog metadata.

May include:
- title
- type
- language
- access_level
- tags
- notes

---

## original/ (Preservation Masters)

```
original/
  pages/
    page_0001.tif
    page_0002.tif
    ...
```

### Rules
- Files are write-once after ingest.
- Pages are renamed deterministically.
- Original file formats are preserved when possible.
- Any correction or rescan requires creating a new archived item.

This directory represents the **archival source of truth**.

---

## derivatives/ (Access Copies)

```
derivatives/
  pdf/
    access_v1.pdf
  images/
    web/
      page_0001.jpg
      ...
    thumb/
      page_0001.jpg
      ...
```

### Notes
- `access_v1.pdf` is the default reading/download PDF.
- `web/` images are optimized for fast UI viewing.
- `thumb/` images are optimized for previews.
- All derivative content can be regenerated from `original/`.

---

## ocr/ (Optical Character Recognition)

OCR outputs are versioned to allow reprocessing with improved methods.

```
ocr/
  v1/
    ocr.txt
    ocr.json
```

Future versions may include layout-aware OCR:

```
ocr/
  v2/
    layout.json
    ocr.json
    ocr.txt
```

### Notes
- OCR output is never considered authoritative.
- Corrections are layered and non-destructive.

---

## checksums/ (Integrity Verification)

```
checksums/
  sha256.txt
```

At minimum, checksums are recorded for:
- original page images
- generated access PDF
- OCR outputs

Checksums allow:
- corruption detection
- backup verification
- provenance validation

---

## events/ (Operational History)

Append-only event records documenting actions taken on the archived item.

```
events/
  2026-01-09T21-15-03Z_ingest_started.json
  2026-01-09T21-18-44Z_ingest_completed.json
  2026-01-09T21-20-10Z_ocr_completed.json
```

These records are useful for debugging, auditing, and historical traceability.

---

## Design Principles Summary

- Originals are sacred and immutable.
- Derivatives and OCR are disposable and rebuildable.
- Metadata and content are cleanly separated.
- Every item is independently verifiable.
- The archive tolerates incomplete metadata without losing structure.

---

## Status

This document defines **Folder Structure v1**.

Any breaking changes must result in a new documented version.
