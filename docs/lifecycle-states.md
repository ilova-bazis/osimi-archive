# Lifecycle States (v1)

This document defines the **lifecycle states** for an archived item (`OBJ-...`) in the Osimi Archive.

Lifecycle states are used to:
- Drive automation workflows (ingest → derivatives → OCR → indexing)
- Power UI queues (e.g., “Needs Review”)
- Make failures recoverable and observable
- Support gradual access changes (private → family → public)

> Note: The on-disk source of truth is `original/` + `meta/ingest.json`.
> Lifecycle states describe processing and review readiness, not file validity.

---

## State model overview

An item moves through two parallel tracks:

1) **Processing track** (automation)
2) **Curation track** (human review)

These tracks are coordinated but intentionally separable: scanning must never be blocked by curation.

---

## Canonical states (v1)

### Processing states (automation)

| State | Meaning | Entry condition | Exit condition |
|---|---|---|---|
| `queued` | Batch detected, job created | Batch marked ready (`DONE` marker or UI submit) | Worker begins ingest |
| `ingesting` | Moving/renaming masters, writing `ingest.json`, checksums | Ingest job started | Ingest completed or failed |
| `ingested` | Masters safely stored and immutable | `meta/ingest.json` written atomically and checksums recorded | Derivatives job begins |
| `derivatives_running` | Creating access PDF and view images | Derivatives job started | Derivatives completed or failed |
| `derivatives_done` | Access PDF + web/thumb images exist (or explicitly skipped) | Derivatives job completed | OCR job begins (optional) |
| `ocr_running` | OCR processing underway | OCR job started | OCR completed or failed |
| `ocr_done` | OCR outputs exist for current OCR version | OCR job completed | Index job begins (optional) |
| `index_running` | Search indexing underway | Index job started | Index completed or failed |
| `index_done` | Search index updated for this object | Index job completed | Ready for human review queues |

### Processing terminal states

| State | Meaning | Notes |
|---|---|---|
| `processing_failed` | One or more automation steps failed | Item remains recoverable; error logged; can retry step-by-step |
| `processing_skipped` | Some steps intentionally skipped | E.g. OCR disabled for a batch; still reviewable |

---

### Curation states (human)

| State | Meaning | Entry condition | Exit condition |
|---|---|---|---|
| `needs_review` | Missing minimum catalog metadata | Ingest completed; `meta/catalog.json` absent or incomplete | User saves valid `catalog.json` |
| `review_in_progress` | Being edited by a user | User opens item for editing (optional lock) | User saves or cancels |
| `reviewed` | Minimum metadata complete | Valid `meta/catalog.json` exists | Access level may change later |
| `curation_failed` | Manual review blocked by validation | E.g. invalid tags, invalid type, missing title | User fixes and saves |

### “Published” visibility (policy, not processing)

Visibility is controlled by `meta/catalog.json`:

- `access.level = private | family | public`

This is independent from processing status.  
An item can be processed and reviewed but still remain private.

---

## Minimum requirements for `reviewed` (v1)

An item is considered **reviewed** when `meta/catalog.json` exists and passes validation:

Required fields:
- `schema_version`
- `object_id` matches folder name
- `access.level` is one of `private|family|public`
- `title.primary` is non-empty
- `classification.type` is valid (or `other`)
- `classification.language` is one of `tg|fa|ru|en`
- `classification.tags` has no duplicates (empty list allowed, but discouraged)
- `updated_at` is present

If any check fails → `curation_failed`.

---

## Recommended queue definitions (v1 UI)

- **Ingest Queue**: `queued`, `ingesting`
- **Processing Queue**: `derivatives_running`, `ocr_running`, `index_running`
- **Needs Review Queue**: `index_done` (or `ocr_done` if indexing disabled) AND curation state = `needs_review`
- **Reviewed Queue**: curation state = `reviewed` (any access level)
- **Failures Queue**: `processing_failed` OR `curation_failed`

---

## State transitions (v1)

### Typical happy path

1. `queued`
2. `ingesting`
3. `ingested`
4. `derivatives_running`
5. `derivatives_done`
6. `ocr_running`
7. `ocr_done`
8. `index_running`
9. `index_done`
10. `needs_review`
11. `reviewed`

### Failure and retry

- Any step can move to `processing_failed`.
- Retrying a failed step should be **idempotent**:
  - If outputs already exist, worker verifies and skips or regenerates safely.
- When a retry succeeds, state returns to the next appropriate state (e.g. `derivatives_done`).

---

## Implementation notes (v1)

### Suggested storage in DB

Store two columns per object:
- `processing_state`
- `curation_state`

And additional tracking fields:
- `last_error`
- `attempt_count`
- `updated_at`

### Locking (optional v1)

To avoid two family members editing the same item at once:
- Use an optimistic lock based on `updated_at`, or
- A short-lived “editing lock” in DB (`locked_by`, `locked_until`)

v1 can start without locking if only one person curates.

---

## Why two-track states

Separating automation from human curation ensures:
- You can scan continuously without blocking on metadata.
- Processing can be rerun or improved later.
- Family review can proceed gradually and safely.

---

## Status

This document defines **Lifecycle States v1**.

Any breaking changes must be documented as a new version.
