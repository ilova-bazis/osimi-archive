# API Reference

This document is the practical route reference for the Osimi backend.

## Conventions

- Base path: `/`
- JSON APIs return `application/json` unless noted.
- All timestamps are ISO-8601 strings.
- Pagination defaults: `limit=50`, max `limit=200`.
- Responses include header `x-request-id`.

## Authentication Modes

### 1) Client session token (UI / API clients)

- Header: `Authorization: Bearer <token>`
- Token is obtained from `POST /api/auth/login`.
- Tenant scope is derived from the authenticated user membership.
- Invalid/missing/expired session resolves to `401 UNAUTHORIZED`.

### 2) Worker shared token (worker control APIs)

- Header: `x-worker-auth-token: <WORKER_AUTH_TOKEN>`
- Optional header: `x-worker-id: <worker-id>`
- Used by worker/internal control endpoints (including ingestion lease/event routes, internal available-files sync, and object-download worker routes).
- Missing/invalid token resolves to `401 UNAUTHORIZED`.

### 3) Signed token URLs (upload/download transport)

- Upload and download transport endpoints use signed URL tokens in path.
- These endpoints do not require bearer token headers.
- Invalid/expired token resolves to `401 UNAUTHORIZED`.

## Standard Error Shape

On non-2xx responses, JSON errors follow this shape:

```json
{
  "request_id": "uuid",
  "error": {
    "code": "BAD_REQUEST",
    "message": "Human readable message"
  }
}
```

`error.details` is optional and included only when provided by the server.

Error codes: `BAD_REQUEST`, `UNAUTHORIZED`, `FORBIDDEN`, `NOT_FOUND`, `METHOD_NOT_ALLOWED`, `CONFLICT`, `CONFIGURATION_ERROR`, `INTERNAL_SERVER_ERROR`.

Planned addition: `LOCKED` (for HTTP `423` state-based non-deliverable conditions).

---

## Public / System

### GET `/healthz`

- Auth: none
- Returns service health.
- 200 response:
  - `status`, `service`, `request_id`, `timestamp`

### PUT `/api/uploads/:token`

- Auth: signed URL token in path (no bearer required)
- Purpose: upload file bytes to staging using a presigned token.
- Required headers:
  - `content-type` must match signed token constraints by media type (parameters are ignored)
  - `content-length` is required and must exactly match signed token constraints
- Body: raw file bytes; byte length must exactly match signed token constraints
- 200 response:
  - `status`, `ingestion_id`, `file_id`, `size_bytes`
- Error behavior:
  - `400 BAD_REQUEST` for header/body constraint mismatch
  - `401 UNAUTHORIZED` for invalid/expired signed token

---

## Client APIs (Bearer token)

## Auth

### POST `/api/auth/login`

- Auth: none
- Body:
  - `username` (string; required)
  - `password` (string; required)
  - `tenant_id` (optional string)
    - required when username is valid across multiple active tenant memberships
    - if provided as empty/whitespace, treated as omitted
- 200 response:
  - `token`, `token_type` (`Bearer`), `user { id, username, tenant_id, role }`
  - `role` is one of `viewer|archiver|admin`
- Error behavior:
  - `400 BAD_REQUEST` for invalid body shape or when `tenant_id` is required but missing for multi-tenant account
  - `401 UNAUTHORIZED` for invalid credentials

### POST `/api/auth/logout`

- Auth: Bearer token
- 200 response:
  - `status` (`ok`), `request_id`
- Error behavior:
  - `401 UNAUTHORIZED` for missing/invalid/expired session token

### GET `/api/auth/me`

- Auth: Bearer token
- 200 response:
  - `user { id, username, tenant_id, role }`
  - `role` is one of `viewer|archiver|admin`
- Error behavior:
  - `401 UNAUTHORIZED` for missing/invalid/expired session token

## Dashboard

### GET `/api/dashboard/summary`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `summary`:
    - `total_ingestions` (number, integer >= 0)
    - `total_objects` (number, integer >= 0)
    - `processed_today` (number, integer >= 0)
      - ingestions where `status = COMPLETED` and `updated_at >= date_trunc('day', now())`
    - `processed_week` (number, integer >= 0)
      - ingestions where `status = COMPLETED` and `updated_at >= date_trunc('week', now())`
    - `failed_count` (number, integer >= 0)
- Error behavior:
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed

### GET `/api/dashboard/activity`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- Query params:
  - `limit` (optional int, default `50`, min `1`, max `200`)
  - `cursor` (optional opaque base64url string)
    - when present, must decode to `{ created_at: string, id: string }`
  - `ingestion_id` (optional UUID; filters activity to a single ingestion)
- Ordering:
  - newest first: `created_at DESC`, then `id DESC`
- 200 response:
  - `activity[]` where each item includes:
    - `id` (string)
    - `event_id` (string)
    - `type` (string)
    - `ingestion_id` (string, nullable)
    - `object_id` (string, nullable)
    - `payload` (JSON object)
    - `actor_user_id` (string, nullable)
    - `created_at` (ISO timestamp string)
  - `next_cursor` (string or `null`)
    - `null` means there are no additional pages
- Error behavior:
  - `400 BAD_REQUEST` for invalid query params (`limit`, `cursor`, `ingestion_id`)
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed

## Ingestions

Ingestion response shapes in this section are authoritative with `src/validation/ingestion.ts`.

### Ingestion Schemas

### Ingestion Schema

- `id` (string)
- `batch_label` (string)
- `tenant_id` (string)
- `status` (`DRAFT|UPLOADING|QUEUED|PROCESSING|COMPLETED|COMPLETED_WITH_ERRORS|FAILED|CANCELED`)
- `created_by` (string)
- `schema_version` (string)
- `classification_type` (`newspaper_article|magazine_article|book_chapter|book|letter|speech|interview|report|manuscript|image|document|other`)
- `item_kind` (`photo|audio|video|scanned_document|document|other`)
- `language_code` (string)
- `pipeline_preset` (`auto|none|ocr_text|audio_transcript|video_transcript|ocr_and_audio_transcript|ocr_and_video_transcript`)
- `access_level` (`private|family|public`)
- `embargo_until` (ISO timestamp string, nullable)
- `rights_note` (string, nullable)
- `sensitivity_note` (string, nullable)
- `summary` (catalog summary object; strict schema)
- `error_summary` (JSON object)
- `created_at` (ISO timestamp string)
- `updated_at` (ISO timestamp string)

### Ingestion File Schema

- `id` (string)
- `ingestion_id` (string)
- `filename` (string)
- `content_type` (string)
- `size_bytes` (number)
- `storage_key` (string)
- `status` (`PENDING|UPLOADED|VALIDATED|FAILED`)
- `checksum_sha256` (string, nullable)
- `processing_overrides` (strict object):
  - optional keys: `ocr_text`, `audio_transcript`, `video_transcript`
  - each value: `{ enabled: boolean, language?: string }`
- `error` (JSON object)
- `created_at` (ISO timestamp string)
- `updated_at` (ISO timestamp string)

### Ingestion Item Schema

- `id` (string)
- `ingestion_id` (string)
- `item_index` (number; integer >= 1)
- `status` (`PENDING|READY|PROCESSING|COMPLETED|FAILED|SKIPPED`)
- `classification_type` (`newspaper_article|magazine_article|book_chapter|book|letter|speech|interview|report|manuscript|image|document|other`, nullable)
- `item_kind` (`photo|audio|video|scanned_document|document|other`, nullable)
- `language_code` (string, nullable)
- `title` (string, nullable)
- `summary` (JSON object)
- `error_summary` (JSON object)
- `object_id` (string, nullable)
- `created_at` (ISO timestamp string)
- `updated_at` (ISO timestamp string)

### Ingestion Item File Schema

- `id` (string)
- `ingestion_item_id` (string)
- `ingestion_file_id` (string)
- `ingestion_id` (string)
- `role` (`primary|front|back|page|attachment|transcript_source|side_a|side_b|other`)
- `sort_order` (number; integer >= 1)
- `page_number` (number, nullable)
- `is_primary` (boolean)
- `logical_label` (string, nullable)
- `created_at` (ISO timestamp string)

### POST `/api/ingestions`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body:
  - `batch_label` (string, required, non-empty)
  - `schema_version` (string literal `"1.0"`; optional, defaults to `"1.0"`)
  - `classification_type` (string; one of `newspaper_article`, `magazine_article`, `book_chapter`, `book`, `letter`, `speech`, `interview`, `report`, `manuscript`, `image`, `document`, `other`)
  - `item_kind` (string; one of `photo`, `audio`, `video`, `scanned_document`, `document`, `other`)
  - `language_code` (string, non-empty; unrestricted)
  - `pipeline_preset` (string; one of `auto`, `none`, `ocr_text`, `audio_transcript`, `video_transcript`, `ocr_and_audio_transcript`, `ocr_and_video_transcript`)
  - `access_level` (string; `private`, `family`, `public`)
  - `embargo_until` (optional RFC3339 timestamp, nullable)
  - `rights_note` (optional string, nullable)
  - `sensitivity_note` (optional string, nullable)
  - `summary` (object; ingestion-stage metadata, based on `catalog.json` fields)
    - required shape (strict, unknown keys rejected):
      - `title`
        - `primary` (string, non-empty)
        - `original_script` (string or `null`)
        - `translations` (array of `{ lang, text }`)
      - `classification`
        - `tags` (string array, unique values)
        - `summary` (string or `null`)
      - `dates`
        - `published` and `created`
          - `value` (YYYY, YYYY-MM, YYYY-MM-DD, or `null`)
          - `approximate` (boolean)
          - `confidence` (`low`, `medium`, `high`)
          - `note` (string or `null`)
    - optional fields:
      - `processing`
        - `ocr_text`, `audio_transcript`, `video_transcript` (each optional `{ enabled, language? }`)
      - `publication` (`name`, `issue`, `volume`, `pages`, `place`)
      - `people` (`subjects`, `authors`, `contributors`, `mentioned`)
      - `links` (`related_object_ids`, `external_urls`)
      - `notes` (`internal`, `public`)
- 201 response:
  - `ingestion` (Ingestion Schema)
- Error behavior:
  - `400 BAD_REQUEST` for invalid body/field validation
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed

### GET `/api/ingestions`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- Query params:
  - `limit` (optional int, default `50`, min `1`, max `200`)
  - `cursor` (optional opaque base64url string)
    - when present, must decode to `{ created_at: ISO timestamp, id: UUID }`
- Ordering:
  - newest first: `created_at DESC`, then `id DESC`
- 200 response:
  - `ingestions[]` (array of Ingestion Schema)
  - `next_cursor` (string or `null`)
- Error behavior:
  - `400 BAD_REQUEST` for invalid query params (`limit`, `cursor`)
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed

### GET `/api/ingestions/:id`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `ingestion` (Ingestion Schema)
  - `files[]` (array of Ingestion File Schema)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion does not exist in tenant scope

### POST `/api/ingestions/:id/items`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body:
  - `item_index` (number, required, integer >= 1)
  - `classification_type` (optional)
  - `item_kind` (optional)
  - `language_code` (optional string, non-empty)
  - `title` (optional string, non-empty)
  - `summary` (optional JSON object)
- Preconditions:
  - ingestion status is `DRAFT`, `UPLOADING`, or `CANCELED`
  - ingestion has no active lease
- 201 response:
  - `item` (Ingestion Item Schema)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body shape
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion does not exist in tenant scope
  - `409 CONFLICT` when ingestion cannot be modified in current status or has active lease

### GET `/api/ingestions/:id/items`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `items[]` (array of Ingestion Item Schema)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path params
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion does not exist in tenant scope

### PATCH `/api/ingestions/:id/items/order`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body:
  - `items[]` (required, non-empty)
    - each item: `{ ingestion_item_id, item_index }`
- Semantics:
  - full-set reorder; payload must contain exactly all current ingestion items
  - `item_index` values must be unique and >= 1
- Preconditions:
  - ingestion status is `DRAFT`, `UPLOADING`, or `CANCELED`
  - ingestion has no active lease
- 200 response:
  - `items[]` (array of Ingestion Item Schema, ordered by `item_index`)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body shape
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `409 CONFLICT` for full-set mismatch or immutable ingestion state

### PATCH `/api/ingestions/:id/items/:itemId`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body (merge semantics; at least one field):
  - `classification_type` (optional)
  - `item_kind` (optional)
  - `language_code` (optional string, non-empty)
  - `title` (optional string or `null`)
  - `description` (optional string or `null`; stored in `summary.classification.summary`)
  - `tags` (optional string[]; normalized and de-duplicated)
  - `dates` (optional object):
    - `published` and/or `created` (partial patch allowed)
      - `value` (YYYY, YYYY-MM, YYYY-MM-DD, or `null`)
      - `approximate` (boolean)
      - `confidence` (`low`, `medium`, `high`)
      - `note` (string or `null`)
- Semantics:
  - merges into existing item `summary`; unspecified fields are preserved
- Preconditions:
  - ingestion status is `DRAFT`, `UPLOADING`, or `CANCELED`
  - ingestion has no active lease
- 200 response:
  - `item` (Ingestion Item Schema)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body shape
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion/item does not exist in tenant scope
  - `409 CONFLICT` when ingestion cannot be modified in current status or has active lease

### PATCH `/api/ingestions/:id`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body (partial update, optional fields):
  - `batch_label` (string)
  - `classification_type` (string; one of `newspaper_article`, `magazine_article`, `book_chapter`, `book`, `letter`, `speech`, `interview`, `report`, `manuscript`, `image`, `document`, `other`)
  - `item_kind` (string; one of `photo`, `audio`, `video`, `scanned_document`, `document`, `other`)
  - `language_code` (string, non-empty; unrestricted)
  - `pipeline_preset` (string; one of `auto`, `none`, `ocr_text`, `audio_transcript`, `video_transcript`, `ocr_and_audio_transcript`, `ocr_and_video_transcript`)
  - `access_level` (string; `private`, `family`, `public`)
  - `embargo_until` (optional RFC3339 timestamp, nullable)
  - `rights_note` (optional string, nullable)
  - `sensitivity_note` (optional string, nullable)
  - `summary` (object; ingestion-stage metadata, based on `catalog.json` fields)
    - full object replacement (same strict schema as `POST /api/ingestions`)
    - partial nested updates are not supported; send the complete summary block
- Preconditions:
  - ingestion status is `DRAFT`, `UPLOADING`, or `CANCELED`
  - ingestion has not started processing (no active lease)
  - request body must include at least one updatable field
- 200 response:
  - `ingestion` (Ingestion Schema)
- Error behavior:
  - `400 BAD_REQUEST` for invalid body/field validation or empty patch body
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion does not exist in tenant scope
  - `409 CONFLICT` when ingestion cannot be modified in current status or has active lease

### DELETE `/api/ingestions/:id`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Preconditions:
  - ingestion status is `DRAFT`, `UPLOADING`, or `CANCELED`
  - ingestion has not started processing (no active lease)
- 200 response:
  - `status` = `deleted`
  - `ingestion_id`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion does not exist in tenant scope
  - `409 CONFLICT` when ingestion cannot be deleted in current status or has active lease

### GET `/api/ingestions/capabilities`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `media_kinds` (array of strings; current values: `image`, `audio`, `video`, `document`)
  - `extensions_by_kind`:
    - `image[]`, `audio[]`, `video[]`, `document[]` (array of strings)
  - `mime_by_kind`:
    - `image[]`, `audio[]`, `video[]`, `document[]` (array of strings)
  - `mime_aliases` (string map from alias MIME to canonical MIME)
- Error behavior:
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed

### POST `/api/ingestions/:id/files/presign`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body (new file):
  - `filename` (string, required, non-empty)
  - `content_type` (string, required, non-empty)
  - `size_bytes` (number, required, integer >= 1)
- Body (re-presign existing):
  - `file_id` (UUID)
- Body must match exactly one shape above.
- Note:
  - item-level file ordering is managed via ingestion item file APIs (`sort_order`), not via presign payload.
- Behavior:
  - if ingestion is `CANCELED`, backend reopens it to `DRAFT` (no files) or `UPLOADING` (has files) before presign flow
  - re-presign is rejected when target file is already committed (`UPLOADED` or `VALIDATED`)
- 201 response:
  - `file_id`, `storage_key`, `upload_url`, `expires_at`, `headers { content-type, content-length }`
  - presigned upload TTL is 1 hour
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body shape
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion or provided `file_id` does not exist in tenant scope
  - `409 CONFLICT` when ingestion cannot accept files in current status or file is already committed

### DELETE `/api/ingestions/:id/files/:fileId`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Preconditions:
  - ingestion is in `DRAFT` or `UPLOADING`
  - if ingestion is `CANCELED`, backend reopens it first (`DRAFT` or `UPLOADING`), then applies delete
- 200 response:
  - `status` = `deleted`
  - `file_id`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path params
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion or file does not exist in tenant scope
  - `409 CONFLICT` when ingestion cannot be modified in current status

### POST `/api/ingestions/:id/files/:fileId/overrides`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body:
  - `processing_overrides` (strict object)
    - optional keys: `ocr_text`, `audio_transcript`, `video_transcript`
    - each value: `{ enabled: boolean, language?: string }`
- Preconditions:
  - ingestion is in `DRAFT` or `UPLOADING`
  - if ingestion is `CANCELED`, backend reopens it first (`DRAFT` or `UPLOADING`)
- 200 response:
  - `file` (Ingestion File Schema)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body shape
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion or file does not exist in tenant scope
  - `409 CONFLICT` when ingestion cannot be modified in current status

### POST `/api/ingestions/:id/files/commit`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body:
  - `file_id` (UUID)
  - `checksum_sha256` (string, 64-char hex)
- Preconditions:
  - all ingestion files must share a single media kind (`image`, `audio`, `video`, `document`)
  - each file `content_type` must map to a supported media kind from ingestion capabilities
- 200 response:
  - `file` (Ingestion File Schema)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body shape
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion or file does not exist in tenant scope
  - `409 CONFLICT` for uncommittable state, missing staged upload, media-kind mismatch, size mismatch, or checksum mismatch

### POST `/api/ingestions/:id/items/:itemId/files`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body:
  - `ingestion_file_id` (UUID, required)
  - `role` (optional; `primary|front|back|page|attachment|transcript_source|side_a|side_b|other`)
  - `sort_order` (number, required, integer >= 1)
  - `page_number` (optional number, integer >= 1)
  - `is_primary` (optional boolean)
  - `logical_label` (optional string, non-empty)
- Preconditions:
  - ingestion status is `DRAFT`, `UPLOADING`, or `CANCELED`
  - ingestion has no active lease
- 201 response:
  - `file` (Ingestion Item File Schema)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body shape
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion/item does not exist in tenant scope
  - `409 CONFLICT` when ingestion cannot be modified in current status, has active lease, or ordering constraints are violated

### GET `/api/ingestions/:id/items/:itemId/files`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `files[]` (array of Ingestion Item File Schema, ordered by `sort_order`)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path params
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion/item does not exist in tenant scope

### PATCH `/api/ingestions/:id/items/:itemId/files/order`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body:
  - `files[]` (required, non-empty)
    - each file: `{ ingestion_file_id, sort_order }`
- Semantics:
  - full-set reorder; payload must contain exactly all files currently linked to item
  - `sort_order` values must be unique and >= 1
- Preconditions:
  - ingestion status is `DRAFT`, `UPLOADING`, or `CANCELED`
  - ingestion has no active lease
- 200 response:
  - `files[]` (array of Ingestion Item File Schema, ordered by `sort_order`)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body shape
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `409 CONFLICT` for full-set mismatch or immutable ingestion state

### POST `/api/ingestions/:id/submit`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Preconditions:
  - at least one file exists
  - at least one file is committed (`UPLOADED` or `VALIDATED`)
  - status transition to `QUEUED` must be allowed by ingestion state machine
- 200 response:
  - `ingestion` (status transitions to queued flow)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion does not exist in tenant scope
  - `409 CONFLICT` when preconditions fail or transition is not allowed

### POST `/api/ingestions/:id/cancel`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Preconditions:
  - ingestion has not started processing (no active lease)
- 200 response:
  - `ingestion`
  - behavior:
    - `QUEUED` transitions to `UPLOADING`
    - `DRAFT` or `UPLOADING` transitions to `CANCELED`
    - `CANCELED` is returned unchanged
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion does not exist in tenant scope
  - `409 CONFLICT` when ingestion cannot be canceled in current status or has active lease

### POST `/api/ingestions/:id/restore`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Preconditions:
  - ingestion status is `CANCELED`
  - ingestion has not started processing (no active lease)
- 200 response:
  - `ingestion` (status transitions to draft or uploading based on files)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion does not exist in tenant scope
  - `409 CONFLICT` when status is not `CANCELED` or active lease exists

### POST `/api/ingestions/:id/retry`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- 200 response:
  - `ingestion` (status transitions to `QUEUED`)
- Behavior:
  - transition follows ingestion state machine rules
  - commonly used for `FAILED -> QUEUED`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when ingestion does not exist in tenant scope
  - `409 CONFLICT` when retry transition is not allowed

## Objects

Object response shapes in this section are authoritative with `src/validation/object.ts`.

### Object Schemas

### Base Object Schema

- `id` (string)
- `object_id` (string, format `OBJ-YYYYMMDD-XXXXXX`)
- `thumbnail_artifact_id` (string, nullable)
- `tenant_id` (string)
- `type` (`GENERIC|IMAGE|AUDIO|VIDEO|DOCUMENT`)
- `title` (string)
- `language` (string, nullable)
- `tags` (array of strings)
- `metadata` (JSON object)
- `source_ingestion_id` (string, nullable)
- `source_batch_label` (string, nullable)
- `processing_state`:
  - `queued|ingesting|ingested|derivatives_running|derivatives_done|ocr_running|ocr_done|index_running|index_done|processing_failed|processing_skipped`
- `curation_state`:
  - `needs_review|review_in_progress|reviewed|curation_failed`
- `availability_state`:
  - `AVAILABLE|ARCHIVED|RESTORE_PENDING|RESTORING|UNAVAILABLE`
- `access_level`:
  - `private|family|public`
- `embargo_kind`:
  - `none|timed|curation_state`
- `embargo_until` (ISO timestamp string, nullable)
- `embargo_curation_state` (`needs_review|review_in_progress|reviewed|curation_failed`, nullable)
- `rights_note` (string, nullable)
- `sensitivity_note` (string, nullable)
- `created_at` (ISO timestamp string)
- `updated_at` (ISO timestamp string)

### Object List Item Schema

`object_list_item = base object +`

- `can_download` (boolean)
- `access_reason_code`:
  - `OK|FORBIDDEN_POLICY|EMBARGO_ACTIVE|RESTORE_REQUIRED|RESTORE_IN_PROGRESS|TEMP_UNAVAILABLE`

### Object Detail Schema

`object_detail = base object +`

- `ingest_manifest` (JSON object, nullable)
- `is_authorized` (boolean)
- `is_deliverable` (boolean)
- `can_download` (boolean)
- `access_reason_code`:
  - `OK|FORBIDDEN_POLICY|EMBARGO_ACTIVE|RESTORE_REQUIRED|RESTORE_IN_PROGRESS|TEMP_UNAVAILABLE`

### GET `/api/objects`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- Query params:
  - `limit` (optional integer, default `50`, min `1`, max `200`)
  - `cursor` (optional opaque cursor from previous response)
    - cursor is sort-aware and must match the requested `sort`
    - required cursor payload by sort:
      - `created_at_desc|created_at_asc`: `{ sort, created_at, object_id }`
      - `updated_at_desc|updated_at_asc`: `{ sort, updated_at, object_id }`
      - `title_asc|title_desc`: `{ sort, title, object_id }`
  - `sort` (optional)
    - allowed: `created_at_desc` (default), `created_at_asc`, `updated_at_desc`, `updated_at_asc`, `title_asc`, `title_desc`
  - `q` (optional text search, minimum guarantee: matches `title`, `object_id`)
  - `availability_state` (optional: `AVAILABLE`, `ARCHIVED`, `RESTORE_PENDING`, `RESTORING`, `UNAVAILABLE`)
  - `access_level` (optional: `private`, `family`, `public`)
  - `language` (optional)
  - `batch_label` (optional)
    - Filters by source ingestion batch label (same dimension returned as `source_batch_label` in list rows).
  - `type` (`GENERIC|IMAGE|AUDIO|VIDEO|DOCUMENT`, optional)
  - `from` (ISO timestamp with offset, optional)
  - `to` (ISO timestamp with offset, optional)
  - `tag` (optional non-empty string)
- 200 response:
  - `objects[]` (`object_list_item[]`; does **not** include `ingest_manifest`)
  - `next_cursor` (string or `null`)
  - `total_count` (total tenant-visible objects before filters)
  - `filtered_count` (total matching current filters)

List row guarantees (`objects[]`): all fields from base object schema, plus `can_download` and `access_reason_code`; excludes `ingest_manifest`.

Sort semantics:

- default sort: `created_at_desc`
- sorting is deterministic
- tie-breaker includes `object_id` for stable cursor paging
- cursor is sort-aware and preserves ordering across pages
- cursor is rejected when payload shape does not match the requested `sort`

Example response:

```json
{
  "objects": [
    {
      "id": "OBJ-20260213-ABC123",
      "object_id": "OBJ-20260213-ABC123",
      "thumbnail_artifact_id": "60000000-0000-4000-8000-000000000777",
      "title": "Document title",
      "processing_state": "queued",
      "curation_state": "needs_review",
      "availability_state": "AVAILABLE",
      "access_level": "private",
      "type": "DOCUMENT",
      "language": "en",
      "tags": ["source:family_archive", "subject:history"],
      "tenant_id": "00000000-0000-0000-0000-000000000001",
      "source_ingestion_id": "13dd3927-17be-4211-9a77-fdea3104a028",
      "source_batch_label": "batch-2026-02-13-001",
      "metadata": {},
      "embargo_kind": "none",
      "embargo_curation_state": null,
      "embargo_until": null,
      "rights_note": null,
      "sensitivity_note": null,
      "can_download": false,
      "access_reason_code": "FORBIDDEN_POLICY",
      "created_at": "2026-02-13T20:22:29.993Z",
      "updated_at": "2026-02-14T08:01:00.000Z"
    }
  ],
  "next_cursor": "...",
  "total_count": 124,
  "filtered_count": 37
}
```

Each object item includes `thumbnail_artifact_id`:

- preferred thumbnail artifact id (`variant = null` preferred, otherwise latest)
- `null` when no thumbnail artifact currently exists
- Error behavior:
  - `400 BAD_REQUEST` for invalid query params (`limit`, `cursor`, `sort`, filters)
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed

### GET `/api/objects/:object_id`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `object` (`object_detail`)
    - includes `tags`
    - includes `ingest_manifest` (or `null`)
    - includes `thumbnail_artifact_id` (`null` when no thumbnail artifact exists)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

Example response:

```json
{
  "object": {
    "id": "OBJ-20260213-ABC123",
    "object_id": "OBJ-20260213-ABC123",
    "thumbnail_artifact_id": "60000000-0000-4000-8000-000000000777",
    "tenant_id": "00000000-0000-0000-0000-000000000001",
    "type": "DOCUMENT",
    "title": "Document title",
    "language": "en",
    "tags": ["source:family_archive", "subject:history"],
    "metadata": {},
    "source_ingestion_id": "13dd3927-17be-4211-9a77-fdea3104a028",
    "source_batch_label": "batch-2026-02-13-001",
    "processing_state": "index_done",
    "curation_state": "needs_review",
    "availability_state": "AVAILABLE",
    "access_level": "private",
    "embargo_kind": "none",
    "embargo_until": null,
    "embargo_curation_state": null,
    "rights_note": null,
    "sensitivity_note": null,
    "created_at": "2026-02-13T20:22:29.993Z",
    "updated_at": "2026-02-14T08:01:00.000Z",
    "ingest_manifest": {
      "schema_version": "1.0"
    },
    "is_authorized": true,
    "is_deliverable": true,
    "can_download": true,
    "access_reason_code": "OK"
  }
}
```

### GET `/api/archive-requests`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- Purpose:
  - unified request visibility endpoint for tenant-wide and object-specific archive tasks
  - use filters instead of separate per-task GET endpoints
- Query params:
  - `limit` (optional int `1..200`, default `50`)
  - `cursor` (optional pagination cursor)
  - `sort` (optional; currently `created_at_desc` only)
  - `target_type` (optional `object|ingestion`)
  - `target_id` (optional string; requires `target_type`; for `target_type=object` must match `OBJ-YYYYMMDD-XXXXXX`)
  - `action_type` (optional `object_resync|artifact_fetch`)
  - `status` (optional; can be repeated and/or comma-separated; values `PENDING|PROCESSING|COMPLETED|FAILED|CANCELED`)
  - `active_only` (optional boolean; when `true`, equivalent to filtering `PENDING|PROCESSING`)
  - `include_payload` (optional boolean, default `false`; when `true`, each item includes `action_payload`)
- 200 response:
  - `requests[]` where each item includes:
    - `id`, `tenant_id`, `target_type`, `target_id`, `action_type`
    - `requested_by`, `dedupe_key`, `status`
    - `failure_reason`, `failure_details`
    - `created_at`, `updated_at`, `completed_at`
    - `action_payload` only when `include_payload=true`
  - `next_cursor` (`string|null`)
  - `filtered_count` (number)
- Error behavior:
  - `400 BAD_REQUEST` for invalid query params/cursor/filter combinations
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed

### PATCH `/api/objects/:object_id`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body:
  - `title` (required string, non-empty)
- Notes:
  - `metadata` patching is intentionally not supported in this phase.
- 200 response:
  - `object` (Base Object Schema)
  - includes resolved `thumbnail_artifact_id` (`null` when no thumbnail artifact exists)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format or invalid body
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

### GET `/api/objects/:object_id/artifacts`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `object_id`
  - `artifacts[]` (`id`, `object_id`, `kind`, `variant`, `storage_key`, `content_type`, `size_bytes`, `created_at`)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

### GET `/api/objects/:object_id/available-files`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `object_id`
  - `available_files[]` (`id`, `object_id`, `archive_file_key`, `artifact_kind`, `variant`, `display_name`, `content_type`, `size_bytes`, `checksum_sha256`, `metadata`, `is_available`, `synced_at`)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

### POST `/api/objects/:object_id/download-requests`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- Body:
  - `available_file_id` (UUID from `GET /api/objects/:object_id/available-files`, required)
- Behavior:
  - If matching artifact already exists on backend (`object_id + artifact_kind + variant`), request is not queued and response returns `status = available` with artifact payload.
  - If artifact is missing, backend queues (or reuses existing active) generic archive request with `action_type = artifact_fetch` and returns `status = queued` with request payload.
- 200 response:
  - when artifact already exists: `status: "available"`, `object_id`, `artifact`
  - when an active request already exists: `status: "queued"`, `object_id`, `request`
- 201 response:
  - when a new queue request is created: `status: "queued"`, `object_id`, `request`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object or selected available file does not exist in tenant scope

### GET `/api/objects/:object_id/download-requests`

- Compatibility read endpoint; prefer `GET /api/archive-requests` for unified request visibility.

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `object_id`
  - `requests[]` (`id`, `object_id`, `available_file_id`, `requested_by`, `artifact_kind`, `variant`, `status`, `failure_reason`, `failure_details`, `created_at`, `updated_at`, `completed_at`)
  - request `status` values: `PENDING|PROCESSING|COMPLETED|FAILED|CANCELED`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

### POST `/api/objects/:object_id/resync`

- Auth: Bearer token
- Roles: `archiver`, `admin`
- Body:
  - optional JSON object
  - `action_payload` (optional object; defaults to `{}`)
- Behavior:
  - creates a generic archive request with:
    - `target_type = object`
    - `target_id = :object_id`
    - `action_type = object_resync`
  - request creation is idempotent for active requests (`PENDING|PROCESSING`) using dedupe key `object_resync:<object_id>`
- 201 response:
  - when new request is created: `status: "queued"`, `object_id`, `request`
- 200 response:
  - when an active request already exists: `status: "queued"`, `object_id`, `request`
- `request` fields:
  - `id`, `tenant_id`, `target_type`, `target_id`, `action_type`, `action_payload`, `requested_by`, `dedupe_key`, `status`, `failure_reason`, `failure_details`, `created_at`, `updated_at`, `completed_at`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format or invalid JSON/body
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

### GET `/api/objects/:object_id/resync-requests`

- Compatibility read endpoint; prefer `GET /api/archive-requests` for unified request visibility.

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- 200 response:
  - `object_id`
  - `requests[]` for `object_resync` action only, each with:
    - `id`, `tenant_id`, `target_type`, `target_id`, `action_type`, `action_payload`, `requested_by`, `dedupe_key`, `status`, `failure_reason`, `failure_details`, `created_at`, `updated_at`, `completed_at`
  - request `status` values: `PENDING|PROCESSING|COMPLETED|FAILED|CANCELED`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

Worker-only object endpoints are documented in `## Worker APIs`:

- `PUT /api/internal/objects/:object_id/available-files`
- `POST /api/archive-requests/lease`
- `POST /api/archive-requests/:id/lease/heartbeat`
- `POST /api/archive-requests/:id/lease/release`
- `POST /api/archive-requests/:id/complete`
- `POST /api/archive-requests/:id/fail`
- `POST /api/object-download-requests/lease`
- `POST /api/object-download-requests/:id/lease/heartbeat`
- `POST /api/object-download-requests/:id/lease/release`
- `POST /api/object-download-requests/:id/artifacts/presign`
- `PUT /api/object-download-requests/uploads/:token`
- `POST /api/object-download-requests/:id/complete`
- `POST /api/object-download-requests/:id/fail`

### GET `/api/objects/:object_id/artifacts/:artifact_id/download`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- Access policy:
  - Endpoint authorization is role-aware and object access-policy aware.
  - A valid role alone does not guarantee download access.
  - `admin` can override access policy restrictions in the current implementation.
- Current behavior:
  - Returns `401 UNAUTHORIZED` for missing/invalid/expired session token.
  - Returns `403 FORBIDDEN` when authenticated role is not allowed.
  - Returns `400 BAD_REQUEST` when object exists but is not downloadable in current policy/deliverability state.
  - Returns `404 NOT_FOUND` when object or artifact does not exist (or is outside tenant scope).
  - Current non-2xx JSON `error.code` values remain from the standard set (for example `BAD_REQUEST`, `NOT_FOUND`).
- Planned behavior (future refinement):
  - Use `403 FORBIDDEN` for policy denial.
  - Use `409 CONFLICT` / `423 LOCKED` for state-based non-deliverable conditions.
  - Keep `404 NOT_FOUND` for missing/non-visible resources.
  - Planned HTTP status refinement does not require changing request/response success payload shape.
- 200 response:
  - Binary file response
  - headers include `content-type`, `content-length`, `content-disposition`

### PATCH `/api/objects/:object_id/access-policy`

- Auth: Bearer token
- Roles: `admin`
- Body:
  - `access_level` (`private|family|public`, required)
  - `embargo_kind` (`none|timed|curation_state`, required)
  - `embargo_until` (ISO timestamp, required when `embargo_kind=timed`)
  - `embargo_curation_state` (`needs_review|review_in_progress|reviewed|curation_failed`, required when `embargo_kind=curation_state`)
  - `rights_note` (optional string, nullable)
  - `sensitivity_note` (optional string, nullable)
- 200 response:
  - `object` (Base Object Schema + `ingest_manifest` nullable)
  - includes resolved `thumbnail_artifact_id` (`null` when no thumbnail artifact exists)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format or invalid body
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

### POST `/api/objects/:object_id/access-requests`

- Auth: Bearer token
- Roles: `viewer`, `archiver`, `admin`
- Body:
  - `requested_level` (`family|private`, required)
  - `reason` (optional string)
- 201 response:
  - `request`:
    - `id`, `object_id`, `requester_user_id`, `requested_level`, `reason`, `status`, `created_at`, `updated_at`
    - `status` values: `PENDING|APPROVED|REJECTED|CANCELED`
- Conflict behavior:
  - returns `409` when the same user already has a `PENDING` request for that object
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format or invalid body
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope
  - `409 CONFLICT` when a pending request already exists for requester+object

### GET `/api/objects/:object_id/access-requests`

- Auth: Bearer token
- Roles: `admin`
- 200 response:
  - `object_id`
  - `requests[]` (`id`, `requester_user_id`, `requested_level`, `reason`, `status`, `reviewed_by`, `reviewed_at`, `decision_note`, `created_at`, `updated_at`)
  - `status` values: `PENDING|APPROVED|REJECTED|CANCELED`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

### POST `/api/objects/:object_id/access-requests/:request_id/approve`

- Auth: Bearer token
- Roles: `admin`
- Body:
  - `decision_note` (optional string)
  - Empty request body is allowed.
- 200 response:
  - `request` (status becomes `APPROVED`)
  - creates or updates assignment for requester
- Conflict behavior:
  - returns `409` if request status is not `PENDING`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object or request does not exist in tenant scope
  - `409 CONFLICT` when request is already resolved

### POST `/api/objects/:object_id/access-requests/:request_id/reject`

- Auth: Bearer token
- Roles: `admin`
- Body:
  - `decision_note` (optional string)
  - Empty request body is allowed.
- 200 response:
  - `request` (status becomes `REJECTED`)
- Conflict behavior:
  - returns `409` if request status is not `PENDING`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object or request does not exist in tenant scope
  - `409 CONFLICT` when request is already resolved

### GET `/api/objects/:object_id/access-assignments`

- Auth: Bearer token
- Roles: `admin`
- 200 response:
  - `object_id`
  - `assignments[]` (`user_id`, `granted_level`, `created_by`, `created_at`)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

### PUT `/api/objects/:object_id/access-assignments`

- Auth: Bearer token
- Roles: `admin`
- Body:
  - `user_id` (UUID, required)
  - `granted_level` (`family|private`, required)
- 200 response:
  - `assignment`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:object_id` format or invalid body
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist in tenant scope

### DELETE `/api/objects/:object_id/access-assignments/:user_id`

- Auth: Bearer token
- Roles: `admin`
- 200 response:
  - `status` (`ok`)
  - `object_id`
  - `user_id`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path params
  - `401 UNAUTHORIZED` for missing/invalid/expired session token
  - `403 FORBIDDEN` when authenticated role is not allowed
  - `404 NOT_FOUND` when object does not exist or assignment is missing in tenant scope

---

## Worker APIs

Worker APIs are for archive/ingestion workers, not UI clients.

Integration guides for archive worker teams:

- `docs/archive-system-integration.md` (ingestion/event pipeline)
- `docs/archive-system-generic-requests.md` (generic archive request queue and worker lease flow)

### Worker Authentication

- Header: `x-worker-auth-token: <WORKER_AUTH_TOKEN>`
- Optional header: `x-worker-id: <worker-id>`
- Missing/invalid worker token: `401 UNAUTHORIZED`
- Missing server configuration for worker token: `500 CONFIGURATION_ERROR`
- Lease-protected ingestion endpoints require a valid body `lease_token` tied to the target ingestion.
- Lease-protected archive-request endpoints require a valid body `lease_token` tied to the target archive request.
- Worker request bodies must not provide `tenant_id`; tenant context is resolved by backend. `tenant_id` in worker lease responses is informational only.

### Ingestion Worker APIs

### POST `/api/ingestions/lease`

- Auth: `x-worker-auth-token` header
- Optional: `x-worker-id`
- Behavior:
  - returns next queued ingestion lease, or `lease: null` when no work is available
  - lease TTL is 5 minutes
  - `items[].files[]` includes ingestion files in `UPLOADED|VALIDATED` status only
- 200 response:
  - `lease: null` when no queued ingestion available
  - or
    - `lease { lease_id, lease_token, lease_expires_at, ingestion_id, batch_label, tenant_id, items[] }`
    - each `items[]` item: `{ ingestion_item_id, item_index, catalog_json, files[] }`
    - each `files[]` item: `{ file_id, filename, sort_order, storage_key, content_type, size_bytes, checksum_sha256, processing_overrides, download_url }`
- `catalog_json` is item-scoped and should be copied per future object
- ordering rule:
  - items: `item_index ASC`
  - files inside item: `sort_order ASC`, then `filename`, then `file_id`
- Error behavior:
  - `401 UNAUTHORIZED` for missing/invalid worker auth token
  - `409 CONFLICT` when queued ingestion metadata is not leasable (for example missing/invalid catalog metadata)
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/ingestions/:id/lease`

- Auth: `x-worker-auth-token` header
- Optional: `x-worker-id`
- Path:
  - `:id` ingestion UUID
- Behavior:
  - deterministic targeted lease acquire for a specific ingestion id
  - no active-lease takeover is allowed (reacquire/recovery path only)
- 200 response:
  - `lease { lease_id, lease_token, lease_expires_at, ingestion_id, batch_label, tenant_id, items[] }`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format
  - `401 UNAUTHORIZED` for missing/invalid worker auth token
  - `404 NOT_FOUND` when ingestion does not exist
  - `409 CONFLICT` when ingestion exists but is not currently leasable (for example active lease or non-`QUEUED` status)
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/ingestions/:id/lease/heartbeat`

- Auth:
  - `x-worker-auth-token` header
  - body `lease_token` must be valid, unexpired, and match `:id`
- Body:
  - `lease_token` (required non-empty string)
- Behavior:
  - extends active lease and returns refreshed `lease_token`
- 200 response:
  - refreshed `lease { lease_id, lease_token, lease_expires_at, ingestion_id, batch_label, tenant_id, items[] }`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format or invalid body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `404 NOT_FOUND` when ingestion does not exist
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/ingestions/:id/lease/release`

- Auth:
  - `x-worker-auth-token` header
  - body `lease_token` must be valid, unexpired, and match `:id`
- Body:
  - `lease_token` (required non-empty string)
- Behavior:
  - releases active lease
  - if ingestion is `PROCESSING`, it is moved back to `QUEUED`
- 200 response:
  - `status` (`ok`), `ingestion_id`, `lease_id`
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format or invalid body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `404 NOT_FOUND` when ingestion does not exist
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### Ingestion Worker Downloads and Events

### GET `/api/worker/downloads/:token`

- Auth: signed download token in path
- 200 response:
  - binary file response
  - headers: `content-type`, `content-length`, `accept-ranges`
- Error behavior:
  - `401 UNAUTHORIZED` for invalid/expired/malformed signed download token
  - `404 NOT_FOUND` when staged file does not exist

### POST `/api/ingestions/:id/events`

- Auth:
  - `x-worker-auth-token` header
  - body `lease_token` must be valid, unexpired, and match `:id`
- Body:
  - `lease_token` (required non-empty string)
  - `events[]` (array; may be empty). Each event includes:
    - `event_id` (UUID)
    - `event_type`:
      - `INGESTION_SUBMITTED`
      - `INGESTION_QUEUED`
      - `INGESTION_PROCESSING`
      - `INGESTION_COMPLETED`
      - `INGESTION_FAILED`
      - `INGESTION_CANCELED`
      - `LEASE_GRANTED`
      - `LEASE_RENEWED`
      - `LEASE_EXPIRED`
      - `LEASE_RELEASED`
      - `FILE_VALIDATED`
      - `FILE_FAILED`
      - `PIPELINE_STEP_STARTED`
      - `PIPELINE_STEP_COMPLETED`
      - `PIPELINE_STEP_FAILED`
      - `INGESTION_ITEM_CREATED`
      - `INGESTION_ITEM_UPDATED`
      - `INGESTION_ITEM_PROCESSING`
      - `INGESTION_ITEM_COMPLETED`
      - `INGESTION_ITEM_FAILED`
      - `OBJECT_CREATED`
      - `ARTIFACT_CREATED`
    - `timestamp` (ISO datetime string with offset)
    - `payload` (JSON object)
    - `ingestion_item_id` (required for `INGESTION_ITEM_*` events)
    - `object_id` (required for `INGESTION_ITEM_COMPLETED|OBJECT_CREATED|ARTIFACT_CREATED`; optional for other event types; when present must match object id format)
- Behavior:
  - idempotent by `event_id`
  - out-of-order tolerant
  - completion event creates or resolves object by source ingestion
  - state projection rules (current phase):
    - `INGESTION_COMPLETED` updates object projection to `processing_state = index_done` and `availability_state = AVAILABLE`
    - `curation_state` is not currently projected from worker event stream in this phase
    - other event types are stored for audit/activity and do not currently mutate object projection fields directly
  - `payload.ingest_json` (when provided and valid object) updates `objects.ingest_manifest` (last-write-wins)
- 200 response:
  - `status`, `ingestion_id`, `inserted_events`, `duplicate_events`, `object_id` (`string|null`)
- Error behavior:
  - `400 BAD_REQUEST` for invalid `:id` format or invalid body/event schema
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `404 NOT_FOUND` when ingestion does not exist
  - `409 CONFLICT` for lease/object conflicts during event ingestion
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### Object Worker APIs

### POST `/api/archive-requests/lease`

- Auth: `x-worker-auth-token`
- Optional: `x-worker-id`
- Body:
  - optional JSON object
  - `action_type` (optional enum): `object_resync|artifact_fetch`
- `tenant_id` is not accepted in request body
- Behavior:
  - sweeps expired archive-request leases before selecting next request
  - lease TTL is 5 minutes
  - when `action_type` is provided, leasing is filtered to that action type only
- 200 response:
  - `request: null` when no pending work
  - otherwise `request` with:
    - `request_id`, `lease_id`, `lease_token`, `lease_expires_at`
    - `tenant_id`, `target_type`, `target_id`, `action_type`, `action_payload`
    - `requested_by`, `dedupe_key`
- Error behavior:
  - `400 BAD_REQUEST` for invalid body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/archive-requests/:id/lease/heartbeat`

- Auth: `x-worker-auth-token`
- Body:
  - `lease_token` (required non-empty string)
- `tenant_id` is not accepted in request body
- 200 response:
  - `request` (`request_id`, `lease_id`, `lease_token`, `lease_expires_at`)
- Behavior:
  - returned `lease_token` is refreshed; use it for subsequent worker calls
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/archive-requests/:id/lease/release`

- Auth: `x-worker-auth-token`
- Body:
  - `lease_token` (required non-empty string)
- `tenant_id` is not accepted in request body
- 200 response:
  - `status: "ok"`, `request_id`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/archive-requests/:id/complete`

- Auth: `x-worker-auth-token`
- Body:
  - `lease_token` (required non-empty string)
- `tenant_id` is not accepted in request body
- 200 response:
  - `status: "completed"`
  - `request` (`id`, `tenant_id`, `target_type`, `target_id`, `action_type`, `action_payload`, `requested_by`, `dedupe_key`, `status`, `failure_reason`, `failure_details`, `created_at`, `updated_at`, `completed_at`)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/archive-requests/:id/fail`

- Auth: `x-worker-auth-token`
- Body:
  - `lease_token` (required non-empty string)
  - `failure` object:
    - `code` (required non-empty string)
    - `message` (required non-empty string)
    - `retryable` (boolean)
    - `details` (object, optional)
- `tenant_id` is not accepted in request body
- Behavior:
  - when omitted, `failure.details` is persisted as `{}`
- 200 response:
  - `status: "failed"`, `request_id`, `retryable`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### PUT `/api/internal/objects/:object_id/available-files`

- Auth: `x-worker-auth-token`
- Path:
  - `:object_id` (format: `OBJ-YYYYMMDD-XXXXXX`)
- Body:
  - `files[]` full-replace snapshot entries (array may be empty):
    - `archive_file_key` (required string, non-empty)
    - `artifact_kind` (required enum):
      - `ingest_json|pipeline_json|catalog_json|original|preview|ocr|transcript|metadata|pdf|ocr_text|thumbnail|web_version|other`
    - `variant` (optional nullable string, non-empty when present)
    - `display_name` (required string, non-empty)
    - `content_type` (optional nullable string, non-empty when present)
    - `size_bytes` (optional nullable number, integer >= 0)
    - `checksum_sha256` (optional nullable string, non-empty when present)
    - `metadata` (object, optional)
    - `is_available` (boolean, optional; defaults `true`)
- Behavior:
  - tenant is resolved internally from `object_id`
  - Replaces object snapshot by archive key: upserts provided entries and marks omitted entries unavailable.
  - Auto-request side effect: backend attempts to auto-queue one `artifact_fetch` request per configured default kind (`thumbnail`, `ocr_text`) when corresponding available entries are present.
  - Auto-request suppression: per kind, backend skips auto-queue when artifact already exists or an active request (`PENDING`/`PROCESSING`) already exists.
  - Auto-request selection priority: for each kind, prefer `variant = null`; otherwise choose lexicographically lowest `archive_file_key`.
- 200 response:
  - `object_id`
  - `synced_files` (number)
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token
  - `404 NOT_FOUND` when object does not exist
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/object-download-requests/lease`

- Auth: `x-worker-auth-token`
- Optional: `x-worker-id`
- Behavior:
  - sweeps expired download-request leases before selecting next request
  - lease TTL is 5 minutes
- 200 response:
  - `request: null` when no pending work
  - otherwise `request` with:
    - `request_id`, `lease_id`, `lease_token`, `lease_expires_at`
    - `object_id`, `tenant_id`, `available_file_id`, `artifact_kind`, `variant`
    - `available_file` (nullable object from available-files snapshot):
      - `id`, `object_id`, `archive_file_key`, `artifact_kind`, `variant`, `display_name`, `content_type`, `size_bytes`, `checksum_sha256`, `metadata`, `is_available`, `synced_at`
- Error behavior:
  - `401 UNAUTHORIZED` for missing/invalid worker auth token
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/object-download-requests/:id/lease/heartbeat`

- Auth: `x-worker-auth-token`
- Body:
  - `lease_token` (required non-empty string)
- 200 response:
  - `request` (`request_id`, `lease_id`, `lease_token`, `lease_expires_at`)
- Behavior:
  - returned `lease_token` is refreshed; use it for subsequent worker calls
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/object-download-requests/:id/lease/release`

- Auth: `x-worker-auth-token`
- Body:
  - `lease_token` (required non-empty string)
- 200 response:
  - `status: "ok"`, `request_id`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/object-download-requests/:id/artifacts/presign`

- Auth: `x-worker-auth-token`
- Body:
  - `lease_token` (required non-empty string)
  - `content_type` (required non-empty string)
  - `size_bytes` (required integer >= 0)
  - `extension` (required non-empty string)
- 200 response:
  - `upload_token`
  - `upload_url` (for `PUT` upload)
  - `storage_key`
  - `expires_at`
  - `headers` (`content-type`, `content-length` where `content-length` is a JSON number)
- Behavior:
  - upload token TTL is 15 minutes
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### PUT `/api/object-download-requests/uploads/:token`

- Auth: none (signed token in path)
- Required headers:
  - `content-type` must match signed token constraints by media type (parameters are ignored)
  - `content-length` is required and must exactly match signed token constraints
- Body: raw file bytes; byte length must exactly match signed token constraints
- 200 response:
  - `status: "ok"`, `request_id`, `size_bytes` (number)
- Error behavior:
  - `400 BAD_REQUEST` for header/body constraint mismatch
  - `401 UNAUTHORIZED` for invalid/expired signed token

### POST `/api/object-download-requests/:id/complete`

- Auth: `x-worker-auth-token`
- Body:
  - `lease_token` (required non-empty string)
  - `upload_token` (required non-empty string)
- Behavior:
  - idempotent success when matching artifact already exists
  - upload token and lease context must match (`request_id`, `object_id`, `tenant_id`, `artifact_kind`, `variant`)
- 200 response:
  - `status: "completed"`, `request_id`, `object_id`, `artifact`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body or lease/upload token context mismatch
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease/upload token
  - `404 NOT_FOUND` when request or uploaded artifact file is not found
  - `409 CONFLICT` when lease is no longer active for completion transition
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

### POST `/api/object-download-requests/:id/fail`

- Auth: `x-worker-auth-token`
- Body:
  - `lease_token` (required non-empty string)
  - `failure` object:
    - `code` (required non-empty string)
    - `message` (required non-empty string)
    - `retryable` (boolean)
    - `details` (object, optional)
- Behavior:
  - when omitted, `failure.details` is persisted as `{}`
- 200 response:
  - `status: "failed"`, `request_id`, `retryable`
- Error behavior:
  - `400 BAD_REQUEST` for invalid path/body
  - `401 UNAUTHORIZED` for missing/invalid worker auth token or invalid/expired lease token
  - `409 CONFLICT` when lease is no longer active
  - `500 CONFIGURATION_ERROR` when worker auth token is not configured server-side

---

## Notes for UI Integrators

- Use client Bearer APIs only; do not call worker endpoints from UI.
- `ingest_manifest` is available on object detail only (`GET /api/objects/:object_id`).
- List endpoints return `next_cursor`; pass it back as `cursor` for pagination.
