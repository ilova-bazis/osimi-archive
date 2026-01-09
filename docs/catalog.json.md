# `meta/catalog.json` Manifest (v1)

This document defines the **human-entered catalog metadata** stored in `meta/catalog.json` for the Osimi Archive.

`catalog.json` is edited by a person (you/family) during **Method 1: one-document-at-a-time review**.
It captures meaning (title, tags, dates, publication context) while `meta/ingest.json` captures ingestion facts.

> Canonical truth for preservation masters remains `original/` + `meta/ingest.json`.
> `catalog.json` is about discovery, context, and access policy.

---

## Location

Within an archived item directory:

```
OBJ-YYYYMMDD-XXXXXX/
  meta/
    catalog.json
```

All paths (if any) in this manifest are **relative to the object root**.

---

## Design goals

- **Fast capture**: allow minimal fields so scanning never blocks
- **Structured tags**: avoid tag chaos across languages
- **Future-friendly**: can later be mirrored into a database and/or UI
- **Non-destructive**: edits never change originals; only metadata evolves
- **Auditability**: track who edited and when

---

## Versioning

- `schema_version` is required (string, e.g. `"1.0"`).
- Breaking changes increment `schema_version`.
- Readers should ignore unknown fields.

---

## Required top-level fields (v1)

| Field | Type | Description |
|---|---|---|
| `schema_version` | string | Schema version, e.g. `"1.0"` |
| `object_id` | string | Must match folder name, e.g. `"OBJ-20260109-000123"` |
| `updated_at` | string (RFC3339 UTC) | Last time this catalog record was updated |
| `updated_by` | string \| null | Who updated it (name or username) |
| `access` | object | Access control for this item |
| `title` | object | Titles and optional translations |
| `classification` | object | Type, language, and tags |
| `dates` | object | Published/created dates (can be unknown/approx) |

---

## `access` object

Defines who can view/download the item.

| Field | Type | Description |
|---|---|---|
| `level` | string | `"private" \| "family" \| "public"` |
| `embargo_until` | string (RFC3339) \| null | Optional; not visible until this date |
| `rights_note` | string \| null | Copyright / permission notes |
| `sensitivity_note` | string \| null | Optional; why restricted |

v1 default: `"private"`.

---

## `title` object

Supports primary title plus translations without forcing them.

| Field | Type | Description |
|---|---|---|
| `primary` | string | The main human-readable title |
| `original_script` | string \| null | Title as printed (if non-Latin) |
| `translations` | array | Optional translations |

### `title.translations[]`

| Field | Type | Description |
|---|---|---|
| `lang` | string | BCP-47-ish code (e.g. `tg`, `fa`, `ru`, `en`) |
| `text` | string | Translated title |

---

## `classification` object

| Field | Type | Description |
|---|---|---|
| `type` | string | Document type (see allowed values) |
| `language` | string | Primary content language |
| `tags` | array[string] | Structured tags, recommended `category:value` format |
| `summary` | string \| null | 1–3 sentence summary (optional) |

### Allowed values (v1) for `classification.type`

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

### Language codes (recommended)

Use a consistent set:
- `tg` (Tajik, Cyrillic assumed by default)
- `fa` (Persian)
- `ru` (Russian)
- `en` (English)

> Note: In this archive, `tg` always implies Tajik written in Cyrillic.
> If non-Cyrillic Tajik or scholarly transliteration is ever needed in the future,
> it must be explicitly indicated (e.g. `tg-Latn`), but this is out of scope for v1.

---

## Tag format (recommended)

Tags should be structured to avoid duplicates and casing problems:

- `topic:education`
- `topic:philosophy`
- `topic:culture`
- `type:tribute`
- `person:muhammad_osimi`
- `place:dushanbe`
- `org:academy_of_sciences`
- `event:anniversary_1990`
- `source:family_archive`

### Tag rules (v1)

1. Tags are lowercase ASCII slugs after the `:` where possible.
2. Use `_` instead of spaces.
3. Keep tags short and stable (don’t encode long sentences).
4. If the printed text is in Tajik/Persian/Russian, store canonical tag in Latin slug and (optionally later) map display labels elsewhere.

---

## `dates` object

| Field | Type | Description |
|---|---|---|
| `published` | object | Publication date information |
| `created` | object | When the content was created (if different) |

### Date object format

| Field | Type | Description |
|---|---|---|
| `value` | string \| null | `"YYYY-MM-DD"` or `"YYYY-MM"` or `"YYYY"` |
| `approximate` | boolean | True if estimated |
| `confidence` | string | `"high" \| "medium" \| "low"` |
| `note` | string \| null | Why it’s approximate / how derived |

This allows unknown/approx dates without breaking structure.

---

## Optional metadata blocks (v1)

These are recommended but not required for v1.

### `publication` (where it appeared)

- `name` (e.g. newspaper/magazine title)
- `issue` (issue number)
- `volume`
- `pages` (e.g. `"3-4"`)
- `place` (city/country)

### `people` (entities)

- `subjects` (e.g. `["muhammad_osimi"]`)
- `authors`
- `contributors`
- `mentioned`

### `links`

- `related_object_ids` (other objects in the archive)
- `external_urls` (public references; optional)

### `notes`

- `internal` (private notes)
- `public` (notes intended for eventual public portal)

---

## Canonical minimal example (v1)

This is the smallest valid `catalog.json` for a newly ingested item:

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
    "primary": "Unknown newspaper article about Muhammad Osimi",
    "original_script": null,
    "translations": []
  },

  "classification": {
    "type": "newspaper_article",
    "language": "tg",
    "tags": [
      "person:muhammad_osimi",
      "source:family_archive"
    ],
    "summary": null
  },

  "dates": {
    "published": { "value": null, "approximate": true, "confidence": "low", "note": "Not yet identified" },
    "created":   { "value": null, "approximate": true, "confidence": "low", "note": "Unknown" }
  }
}
```

---

## Expanded example (v1)

```json
{
  "schema_version": "1.0",
  "object_id": "OBJ-20260109-000123",
  "updated_at": "2026-01-10T00:12:02Z",
  "updated_by": "Farzon",

  "access": {
    "level": "family",
    "embargo_until": null,
    "rights_note": "For family sharing only. Do not republish without consensus.",
    "sensitivity_note": null
  },

  "title": {
    "primary": "A tribute to Muhammad Osimi on the anniversary of his academic service",
    "original_script": "…",
    "translations": [
      { "lang": "en", "text": "A tribute to Muhammad Osimi on the anniversary of his academic service" }
    ]
  },

  "classification": {
    "type": "newspaper_article",
    "language": "tg",
    "tags": [
      "person:muhammad_osimi",
      "topic:education",
      "topic:culture",
      "type:tribute",
      "place:dushanbe",
      "org:academy_of_sciences",
      "source:newspaper_xyz"
    ],
    "summary": "A short commemorative article describing Osimi’s contributions to science and culture, including references to his leadership at the Academy of Sciences."
  },

  "dates": {
    "published": { "value": "1985-03", "approximate": true, "confidence": "medium", "note": "Month inferred from issue header; day not visible" },
    "created":   { "value": null, "approximate": true, "confidence": "low", "note": "Not specified" }
  },

  "publication": {
    "name": "Newspaper XYZ",
    "issue": "No. 12",
    "volume": null,
    "pages": "3",
    "place": "Dushanbe, Tajik SSR"
  },

  "people": {
    "subjects": ["muhammad_osimi"],
    "authors": ["unknown"],
    "contributors": [],
    "mentioned": ["academy_of_sciences"]
  },

  "links": {
    "related_object_ids": ["OBJ-20260108-000100"],
    "external_urls": []
  },

  "notes": {
    "internal": "Scan quality is good; consider layout-aware OCR v2 for multi-column text.",
    "public": null
  }
}
```

---

## Invariants (must hold)

1. `object_id` must match the object directory name.
2. `schema_version` must be present.
3. `access.level` must be one of `private`, `family`, `public`.
4. `classification.type` must be one of the allowed values (or `other`).
5. Tags must be unique within the `tags` array (no duplicates).
6. `updated_at` must update on every edit.

---

## Practical workflow (Method 1)

For each ingested item:

1. Open item in Review UI (or edit `meta/catalog.json` directly in early MVP).
2. Fill:
   - title (primary)
   - type, language
   - 2–7 tags
   - access level (default private)
3. Save → item moves out of “Needs Review”.

Batch operations can be added later, but v1 is intentionally one-at-a-time.

---