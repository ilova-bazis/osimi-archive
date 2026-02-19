# Catalog.json UI Guide (v1)

This document is a **UI-facing guide** for building and validating `meta/catalog.json`.
Use it to ensure the UI writes the correct structure for the Osimi Archive.

---

## Purpose

- `catalog.json` is **human-owned** metadata.
- It describes **what the item is**, not how the pipeline processed it.
- It is safe for ongoing human curation and edits.

`catalog.json` must never include machine outputs or pipeline state.

---

## Location

```
OBJ-YYYYMMDD-XXXXXX/
  meta/
    catalog.json
```

---

## Required Top-level Fields

| Field | Type | Notes |
|---|---|---|
| `schema_version` | string | Must be `"1.0"` |
| `object_id` | string | Must match the object directory name |
| `updated_at` | string | RFC3339 UTC timestamp |
| `updated_by` | string \| null | Person or UI username |
| `access` | object | Access policy block |
| `title` | object | Title block |
| `classification` | object | Type, language, tags |
| `dates` | object | Publication/creation dates |

---

## `access` (required)

```json
"access": {
  "level": "private",
  "embargo_until": null,
  "rights_note": null,
  "sensitivity_note": null
}
```

- `level` must be one of: `private`, `family`, `public`.

---

## `title` (required)

```json
"title": {
  "primary": "Main title",
  "original_script": null,
  "translations": []
}
```

`title.primary` must be non-empty.

---

## `classification` (required)

```json
"classification": {
  "type": "document",
  "language": "tg",
  "tags": ["source:family_archive"],
  "summary": null
}
```

### Allowed `classification.type`

- `newspaper_article`
- `magazine_article`
- `book_chapter`
- `book`
- `photo`
- `letter`
- `speech`
- `interview`
- `document`
- `other`

### Allowed `classification.language`

- `tg`, `fa`, `ru`, `en`

Tags must be unique within the array.

---

## `dates` (required)

```json
"dates": {
  "published": { "value": "YYYY-MM", "approximate": true, "confidence": "low", "note": null },
  "created":   { "value": null, "approximate": true, "confidence": "low", "note": null }
}
```

- `value` can be `YYYY`, `YYYY-MM`, or `YYYY-MM-DD` (or `null`).
- The `dates` object must exist, even if values are unknown.

---

## Optional Processing Intent

These fields **guide the orchestrator** but are still human-owned metadata.

### `item_kind`

```json
"item_kind": "scanned_document"
```

Suggested values:
- `scanned_document`
- `photo`
- `audio`
- `video`
- `document`
- `other`

### `processing`

```json
"processing": {
  "ocr_text": { "enabled": true, "language": "tg" },
  "audio_transcript": { "enabled": false }
}
```

Notes:
- If omitted, defaults are inferred from `item_kind` and `classification.language`.
- This block must **not** include pipeline state or machine outputs.

---

## Minimal Valid Example

```json
{
  "schema_version": "1.0",
  "object_id": "OBJ-20260109-000123",
  "updated_at": "2026-01-09T22:05:11Z",
  "updated_by": "Farzon",
  "access": {
    "level": "private",
    "embargo_until": null,
    "rights_note": null,
    "sensitivity_note": null
  },
  "title": {
    "primary": "Unknown newspaper article",
    "original_script": null,
    "translations": []
  },
  "classification": {
    "type": "newspaper_article",
    "language": "tg",
    "tags": ["source:family_archive"],
    "summary": null
  },
  "dates": {
    "published": { "value": null, "approximate": true, "confidence": "low", "note": "Not yet identified" },
    "created":   { "value": null, "approximate": true, "confidence": "low", "note": "Unknown" }
  }
}
```

---

## Common UI Mistakes

- Missing `dates` block (required, even if unknown)
- Missing `title.primary`
- Invalid `classification.type` or `classification.language`
- Duplicate tags in `classification.tags`
- Including pipeline state or machine outputs

---

## Validation Checklist

- [ ] `schema_version` is present and `"1.0"`
- [ ] `object_id` matches folder name
- [ ] `updated_at` is valid RFC3339 UTC
- [ ] `access.level` is valid
- [ ] `title.primary` is non-empty
- [ ] `classification.type` and `classification.language` are valid
- [ ] `dates` object exists
