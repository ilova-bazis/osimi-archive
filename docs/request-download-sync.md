# Archive-System Integration: Available Files and Download Fulfillment

This document is the implementation contract for archive-system integration with backend object download requests.

It covers:

1. publishing per-object available-file snapshots,
2. user request behavior dependency,
3. worker lease/upload/complete/fail endpoints,
4. exact request/response payloads, validation, and retry semantics.

Normative API reference is `docs/api-reference.md`. This document is aligned with current backend route and validation code.

---

## Core Model

Backend does not accept free-form user requests like "generate pdf".

Flow is deterministic:

1. archive-system publishes available files for an object,
2. backend lists those files to users,
3. user selects one `available_file_id`,
4. backend either returns existing artifact (`available`) or queues/reuses request (`queued`),
5. archive-system worker fulfills queued requests.

---

## Shared Conventions

### Object ID format

- `object_id` path parameter must match: `OBJ-YYYYMMDD-XXXXXX`

### Worker auth

- Required header on worker endpoints: `x-worker-auth-token`
- Value must equal backend `WORKER_AUTH_TOKEN`
- Optional header on lease endpoint: `x-worker-id`

### Error response shape

All non-2xx responses use this envelope:

```json
{
  "request_id": "<server-request-id>",
  "error": {
    "code": "BAD_REQUEST|UNAUTHORIZED|NOT_FOUND|CONFLICT|CONFIGURATION_ERROR|INTERNAL_SERVER_ERROR",
    "message": "Human-readable message",
    "details": {}
  }
}
```

`error.details` is optional.

### Canonical `artifact_kind` values

Use only these values:

- `ingest_json`
- `pipeline_json`
- `catalog_json`
- `original`
- `preview`
- `ocr`
- `transcript`
- `metadata`
- `pdf`
- `ocr_text`
- `thumbnail`
- `web_version`
- `other`

---

## 1) Publish Available Files Snapshot

### Endpoint

`PUT /api/internal/objects/:object_id/available-files`

### Auth

- Worker auth (`x-worker-auth-token`)

### Request body

```json
{
  "files": [
    {
      "archive_file_key": "archive-original-bundle-v1",
      "artifact_kind": "original",
      "variant": null,
      "display_name": "Original Bundle",
      "content_type": "application/pdf",
      "size_bytes": 10485760,
      "checksum_sha256": "b1946ac92492d2347c6235b4d2611184",
      "metadata": {
        "source": "archive-system"
      },
      "is_available": true
    }
  ]
}
```

### Field constraints

- `files`: required array (can be empty)
- `archive_file_key`: required non-empty string
- `artifact_kind`: required enum value
- `variant`: optional; nullable string; if string, non-empty
- `display_name`: required non-empty string
- `content_type`: optional; nullable string; if string, non-empty
- `size_bytes`: optional; nullable integer >= 0
- `checksum_sha256`: optional; nullable string; if string, non-empty
- `metadata`: optional object; defaults to `{}`
- `is_available`: optional boolean; defaults to `true`

### Snapshot semantics (full replace)

Replace scope is `(tenant_id, object_id)` and key is `archive_file_key`.

Behavior:

1. provided files are upserted,
2. previously known keys for the object that are omitted are marked `is_available = false`.

This is not a patch API. Always send the full current file list for that object.

### Tenant behavior

- Do not send tenant ID.
- Backend resolves tenant by `object_id`.

### Success response

`200 OK`

```json
{
  "object_id": "OBJ-20260209-ABC123",
  "synced_files": 3
}
```

### Errors

- `400 BAD_REQUEST`: invalid JSON or field validation error
- `401 UNAUTHORIZED`: missing/invalid worker token
- `404 NOT_FOUND`: object not found
- `500 CONFIGURATION_ERROR`: backend missing worker token config

---

## 2) User-Visible Available Files

### Endpoint

`GET /api/objects/:object_id/available-files`

### Auth

- Bearer token (viewer/archiver/admin)

### Behavior

- Returns only rows where `is_available = true`
- Includes backend-generated `id` used for request creation

### Success response

`200 OK`

```json
{
  "object_id": "OBJ-20260209-ABC123",
  "available_files": [
    {
      "id": "70000000-0000-4000-8000-000000000001",
      "object_id": "OBJ-20260209-ABC123",
      "archive_file_key": "archive-original-bundle-v1",
      "artifact_kind": "original",
      "variant": null,
      "display_name": "Original Bundle",
      "content_type": "application/pdf",
      "size_bytes": 10485760,
      "checksum_sha256": "b1946ac92492d2347c6235b4d2611184",
      "metadata": {
        "source": "archive-system"
      },
      "is_available": true,
      "synced_at": "2026-03-05T10:15:30.000Z"
    }
  ]
}
```

---

## 3) User Download Request Creation Dependency

### Endpoint

`POST /api/objects/:object_id/download-requests`

### Request body

```json
{
  "available_file_id": "70000000-0000-4000-8000-000000000001"
}
```

### Backend behavior

1. selected available file must belong to object/tenant and still be available,
2. backend checks existing artifact by identity `(object_id, artifact_kind, variant)`,
3. if artifact exists -> returns `status = "available"`,
4. if artifact missing -> queue new request or dedupe to existing active request.

### Status code semantics

- `201` new queued request created
- `200` artifact already available
- `200` active queued/processing request already exists (deduped)

### Responses

`200 available`

```json
{
  "status": "available",
  "object_id": "OBJ-20260209-ABC123",
  "artifact": {
    "id": "60000000-0000-4000-8000-000000000099",
    "object_id": "OBJ-20260209-ABC123",
    "kind": "pdf",
    "variant": null,
    "storage_key": "tenants/.../objects/.../artifacts/source.pdf",
    "content_type": "application/pdf",
    "size_bytes": 12345,
    "created_at": "2026-03-05T10:15:30.000Z"
  }
}
```

`201 or 200 queued`

```json
{
  "status": "queued",
  "object_id": "OBJ-20260209-ABC123",
  "request": {
    "id": "90000000-0000-4000-8000-000000000001",
    "object_id": "OBJ-20260209-ABC123",
    "available_file_id": "70000000-0000-4000-8000-000000000001",
    "requested_by": "10000000-0000-0000-0000-000000000002",
    "artifact_kind": "pdf",
    "variant": null,
    "status": "PENDING",
    "failure_reason": null,
    "failure_details": null,
    "created_at": "2026-03-05T10:15:30.000Z",
    "updated_at": "2026-03-05T10:15:30.000Z",
    "completed_at": null
  }
}
```

---

## 4) Worker Fulfillment API (Strict Contract)

### 4.1 Lease next pending request

`POST /api/object-download-requests/lease`

Headers:

- required `x-worker-auth-token`
- optional `x-worker-id`

Success when no work:

```json
{
  "request": null
}
```

Success when leased:

```json
{
  "request": {
    "request_id": "90000000-0000-4000-8000-000000000001",
    "lease_id": "a52ca841-b95d-4a5d-9659-00f2df82e96a",
    "lease_token": "<signed-token>",
    "lease_expires_at": "2026-03-05T10:20:30.000Z",
    "object_id": "OBJ-20260209-ABC123",
    "tenant_id": "00000000-0000-0000-0000-000000000001",
    "available_file_id": "70000000-0000-4000-8000-000000000001",
    "artifact_kind": "pdf",
    "variant": null,
    "available_file": {
      "id": "70000000-0000-4000-8000-000000000001",
      "object_id": "OBJ-20260209-ABC123",
      "archive_file_key": "archive-original-bundle-v1",
      "artifact_kind": "pdf",
      "variant": null,
      "display_name": "Primary PDF",
      "content_type": "application/pdf",
      "size_bytes": 10485760,
      "checksum_sha256": null,
      "metadata": {},
      "is_available": true,
      "synced_at": "2026-03-05T10:15:30.000Z"
    }
  }
}
```

Lease notes:

- lease TTL is 5 minutes,
- backend auto-sweeps expired processing leases back to `PENDING` before selecting next.

### 4.2 Heartbeat lease

`POST /api/object-download-requests/:id/lease/heartbeat`

Request:

```json
{
  "lease_token": "<lease-token-from-lease-or-last-heartbeat>"
}
```

Success:

```json
{
  "request": {
    "request_id": "90000000-0000-4000-8000-000000000001",
    "lease_id": "a52ca841-b95d-4a5d-9659-00f2df82e96a",
    "lease_token": "<refreshed-signed-token>",
    "lease_expires_at": "2026-03-05T10:25:30.000Z"
  }
}
```

Important: use the returned refreshed `lease_token` for subsequent calls.

### 4.3 Release lease (abandon)

`POST /api/object-download-requests/:id/lease/release`

Request:

```json
{
  "lease_token": "<active-lease-token>"
}
```

Success:

```json
{
  "status": "ok",
  "request_id": "90000000-0000-4000-8000-000000000001"
}
```

Behavior: request transitions back to `PENDING`.

### 4.4 Presign object artifact upload

`POST /api/object-download-requests/:id/artifacts/presign`

Request:

```json
{
  "lease_token": "<active-lease-token>",
  "content_type": "text/plain",
  "size_bytes": 11,
  "extension": "txt"
}
```

Field constraints:

- `lease_token`: required non-empty string
- `content_type`: required non-empty string
- `size_bytes`: required integer >= 0
- `extension`: required non-empty string

Success:

```json
{
  "upload_token": "<signed-upload-token>",
  "upload_url": "/api/object-download-requests/uploads/<signed-upload-token>",
  "storage_key": "tenants/.../objects/.../artifacts/<request-id>-pdf.txt",
  "expires_at": "2026-03-05T10:30:30.000Z",
  "headers": {
    "content-type": "text/plain",
    "content-length": 11
  }
}
```

Upload token TTL is 15 minutes.

### 4.5 Upload bytes

`PUT /api/object-download-requests/uploads/:token`

Auth:

- no worker auth header required
- authorization is embedded in `:token`

Required request headers:

- `content-type`: must equal signed token `content_type` (base media type; parameters are ignored)
- `content-length`: required and must equal signed token `size_bytes`

Body rules:

- exact byte length must equal signed token `size_bytes`

Success:

```json
{
  "status": "ok",
  "request_id": "90000000-0000-4000-8000-000000000001",
  "size_bytes": 11
}
```

### 4.6 Complete request

`POST /api/object-download-requests/:id/complete`

Request:

```json
{
  "lease_token": "<lease-token>",
  "upload_token": "<upload-token-returned-by-presign>"
}
```

Success:

```json
{
  "status": "completed",
  "request_id": "90000000-0000-4000-8000-000000000001",
  "object_id": "OBJ-20260209-ABC123",
  "artifact": {
    "id": "60000000-0000-4000-8000-000000000111",
    "object_id": "OBJ-20260209-ABC123",
    "kind": "pdf",
    "variant": null,
    "storage_key": "tenants/.../objects/.../artifacts/...pdf",
    "content_type": "application/pdf",
    "size_bytes": 10485760,
    "created_at": "2026-03-05T10:19:30.000Z"
  }
}
```

Completion semantics:

- idempotent when matching artifact already exists,
- lease is not required to still be active for token authorization, but completion update still conflicts if request is no longer in active leased processing state,
- upload token must still be valid and consistent with lease context.

### 4.7 Fail request

`POST /api/object-download-requests/:id/fail`

Request:

```json
{
  "lease_token": "<active-lease-token>",
  "failure": {
    "code": "FETCH_FAILED",
    "message": "Archive source returned 503",
    "retryable": true,
    "details": {
      "upstream_status": 503,
      "attempt": 2
    }
  }
}
```

Failure constraints:

- `failure.code`: required non-empty string
- `failure.message`: required non-empty string
- `failure.retryable`: required boolean
- `failure.details`: optional object

Success:

```json
{
  "status": "failed",
  "request_id": "90000000-0000-4000-8000-000000000001",
  "retryable": true
}
```

---

## 5) Worker Error and Retry Semantics

### Common status guidance

- `400 BAD_REQUEST`: invalid JSON, invalid schema, or upload header/body constraint mismatch
- `401 UNAUTHORIZED`: missing/invalid worker token, invalid/expired signed lease/upload token
- `404 NOT_FOUND`: object/request/uploaded file not found where applicable
- `409 CONFLICT`: lease no longer active, stale lease transition, or race state conflict
- `500 CONFIGURATION_ERROR`: worker auth not configured server-side
- `500 INTERNAL_SERVER_ERROR`: unexpected server error

### Retry guidance

- On `request: null` from lease: sleep and poll again.
- On `409 CONFLICT` during heartbeat/release/fail/complete: do not blindly retry same transition forever; re-lease work.
- On `401` token-expired errors: obtain fresh lease/presign tokens and restart relevant step.
- On upload `400` mismatch errors: fix payload/header mismatch before retry.
- On transient `5xx`: safe to retry with backoff.

---

## 6) Canonical Worker Sequence

1. `POST /api/object-download-requests/lease`
2. if `request = null` -> poll loop
3. optional periodic `POST /api/object-download-requests/:id/lease/heartbeat` while processing
4. `POST /api/object-download-requests/:id/artifacts/presign`
5. `PUT /api/object-download-requests/uploads/:token` with exact headers/body
6. `POST /api/object-download-requests/:id/complete`
7. on unrecoverable processing failure instead of completion: `POST /api/object-download-requests/:id/fail`
8. on graceful abandonment: `POST /api/object-download-requests/:id/lease/release`

---

## 7) Idempotency and Stability Requirements

- Keep `archive_file_key` stable for the same logical file across syncs.
- Send full snapshot every sync event.
- Snapshot sync retries are safe (upsert + full-replace semantics).
- Request creation dedupes active `(object_id, artifact_kind, variant)` requests.
- Completion is idempotent for already-existing matching artifacts.
- Persist and use latest heartbeat lease token if your worker heartbeats.
