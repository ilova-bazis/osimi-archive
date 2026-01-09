# `meta/ingest.json` Manifest (v1)

This document defines the **`meta/ingest.json`** manifest schema for the Osimi Archive.

`ingest.json` is created automatically by the ingestion pipeline and is **never manually edited**.
It makes every archived item **self-describing**, **portable**, and **rebuildable**.

---

## Location

Within an archived item directory:

```
OBJ-YYYYMMDD-XXXXXX/
  meta/
    ingest.json
```

All paths inside the manifest are **relative to the object root** unless explicitly stated otherwise.

---

## Design goals

- **Single-source manifest** for ingestion facts and outputs
- **Forward compatible**: readers ignore unknown fields
- **Rebuildable**: derivatives/OCR/index can be regenerated from originals using this manifest
- **Auditable**: preserves provenance of source filenames and input location
- **Atomic writes**: must be written via temp file + rename

---

## Versioning

- `schema_version` is required (string, e.g. `"1.0"`).
- Any breaking change increments `schema_version`.
- Minor additive changes should keep the same major version and add fields.

---

## Required top-level fields (v1)

| Field | Type | Description |
|---|---|---|
| `schema_version` | string | Manifest schema version, e.g. `"1.0"` |
| `object_id` | string | Permanent object ID, e.g. `"OBJ-20260109-000123"` |
| `created_at` | string (RFC3339 UTC) | When the manifest file was created |
| `ingest` | object | Ingest session metadata (source, operator, notes) |
| `original` | object | Original masters summary (pages, formats, sizes) |
| `derivatives` | object | Derivative outputs produced (may be empty) |
| `ocr` | object | OCR runs produced (may be empty) |
| `checksums` | object | Checksum files produced (at minimum `sha256.txt`) |
| `tools` | object (optional) | Tool/engine versions used by the pipeline |

---

## `ingest` object

### Required fields

| Field | Type | Description |
|---|---|---|
| `ingest_id` | string | Unique ingest session ID (stable per run) |
| `source` | object | Where the pages came from |
| `operator` | object | Who performed the scanning/ingest (may be unknown) |
| `notes` | string \| null | Optional notes |

### `ingest.source`

| Field | Type | Description |
|---|---|---|
| `type` | string | `"drop_folder" \| "ui_upload" \| "cli_import" \| "scanner_integration"` |
| `path` | string | Absolute path to the source input (informational) |
| `captured_at` | string (RFC3339 UTC) | When capture/scan finished (best-effort) |

### `ingest.operator`

| Field | Type | Description |
|---|---|---|
| `name` | string \| null | Operator name |
| `contact` | string \| null | Optional contact info |

---

## `original` object

### Required fields

| Field | Type | Description |
|---|---|---|
| `pages_dir` | string | Relative path to masters directory (`"original/pages"`) |
| `page_count` | integer | Number of pages ingested |
| `page_naming` | string | Naming pattern (v1: `"page_%04d"`) |
| `page_start` | integer | Start page number (v1: `1`) |
| `format_policy` | string | v1: `"preserve"` |
| `pages` | array | Per-page entries (provenance + sanity checks) |

### `original.pages[]` entry

| Field | Type | Description |
|---|---|---|
| `page_number` | integer | 1-based page number |
| `filename` | string | Stored filename under `original/pages/` |
| `source_filename` | string | Original filename from ingest source |
| `mime_type` | string | MIME type (e.g. `image/tiff`) |
| `bytes` | integer | File size in bytes |

---

## `derivatives` object

Derivatives are **replaceable** outputs generated from originals.

### `derivatives.pdf[]`

| Field | Type | Description |
|---|---|---|
| `kind` | string | `"access" \| "print" \| "preview"` (v1 uses `"access"`) |
| `version` | string | `"v1"` etc. |
| `path` | string | Relative path to the PDF |
| `mime_type` | string | `application/pdf` |
| `bytes` | integer | File size in bytes |
| `source` | object | How it was generated |

### `derivatives.images`

| Field | Type | Description |
|---|---|---|
| `web_dir` | string | Relative web images directory |
| `thumb_dir` | string | Relative thumbnails directory |
| `web_mime_type` | string | MIME type for web images |
| `thumb_mime_type` | string | MIME type for thumbnails |

---

## `ocr` object

OCR runs are versioned so the archive can be reprocessed.

### `ocr.runs[]`

| Field | Type | Description |
|---|---|---|
| `version` | string | `"v1"`, `"v2"`, ... |
| `engine` | object | OCR engine details |
| `outputs` | object | Output paths |
| `status` | string | `"queued" \| "running" \| "completed" \| "failed"` |
| `started_at` | string (RFC3339 UTC) \| null | Start time |
| `finished_at` | string (RFC3339 UTC) \| null | End time |

### `ocr.runs[].engine`

| Field | Type | Description |
|---|---|---|
| `name` | string | OCR engine name (e.g. `tesseract`) |
| `version` | string | Engine version (best-effort) |
| `lang` | array[string] | Language codes used |

### `ocr.runs[].outputs`

| Field | Type | Description |
|---|---|---|
| `txt` | string \| null | Relative path to concatenated text output |
| `json` | string \| null | Relative path to structured output |

---

## `checksums` object

### Required fields

| Field | Type | Description |
|---|---|---|
| `algorithm` | string | v1: `"sha256"` |
| `files` | array | Checksum files produced |

### `checksums.files[]`

| Field | Type | Description |
|---|---|---|
| `path` | string | Relative path to checksum file (e.g. `checksums/sha256.txt`) |
| `covers` | array[string] | What categories it covers (`original`, `derivatives`, `ocr`) |

---

## Canonical example (v1)

```json
{
  "schema_version": "1.0",
  "object_id": "OBJ-20260109-000123",
  "created_at": "2026-01-09T21:18:44Z",
  "ingest": {
    "ingest_id": "ING-20260109-211503Z-4f2c9e",
    "source": {
      "type": "drop_folder",
      "path": "/osimi-archive/ingest_drop/2026-01-09/batch_0001",
      "captured_at": "2026-01-09T21:15:03Z"
    },
    "operator": {
      "name": "Farzon Nosiri",
      "contact": null
    },
    "notes": null
  },
  "original": {
    "pages_dir": "original/pages",
    "page_count": 12,
    "page_naming": "page_%04d",
    "page_start": 1,
    "format_policy": "preserve",
    "pages": [
      {
        "page_number": 1,
        "filename": "page_0001.tif",
        "source_filename": "IMG_1042.TIF",
        "mime_type": "image/tiff",
        "bytes": 18349201
      },
      {
        "page_number": 2,
        "filename": "page_0002.tif",
        "source_filename": "IMG_1043.TIF",
        "mime_type": "image/tiff",
        "bytes": 18211044
      }
    ]
  },
  "derivatives": {
    "pdf": [
      {
        "kind": "access",
        "version": "v1",
        "path": "derivatives/pdf/access_v1.pdf",
        "mime_type": "application/pdf",
        "bytes": 52104432,
        "source": {
          "from_pages": true,
          "page_count": 12
        }
      }
    ],
    "images": {
      "web_dir": "derivatives/images/web",
      "thumb_dir": "derivatives/images/thumb",
      "web_mime_type": "image/jpeg",
      "thumb_mime_type": "image/jpeg"
    }
  },
  "ocr": {
    "runs": [
      {
        "version": "v1",
        "engine": {
          "name": "tesseract",
          "version": "5.x",
          "lang": ["tgk", "rus", "fas"]
        },
        "outputs": {
          "txt": "ocr/v1/ocr.txt",
          "json": "ocr/v1/ocr.json"
        },
        "status": "completed",
        "started_at": "2026-01-09T21:20:00Z",
        "finished_at": "2026-01-09T21:20:10Z"
      }
    ]
  },
  "checksums": {
    "algorithm": "sha256",
    "files": [
      {
        "path": "checksums/sha256.txt",
        "covers": ["original", "derivatives", "ocr"]
      }
    ]
  },
  "tools": {
    "ingest_service": {
      "name": "osimi-archive-ingest",
      "version": "0.1.0"
    },
    "pdf_builder": {
      "name": "img2pdf",
      "version": "0.5.x"
    },
    "image_processor": {
      "name": "vips",
      "version": "8.x"
    }
  }
}
```

---

## Invariants (must hold)

1. `object_id` must match the directory name.
2. `original.page_count` must equal the number of files in `original/pages/`.
3. Page numbers must be contiguous starting at `page_start` (default `1`).
4. Manifest paths are **relative to the object root** (portable).
5. `ingest.source.path` may be absolute (machine-specific) but is informational.
6. `meta/ingest.json` must exist if the object directory exists.

---