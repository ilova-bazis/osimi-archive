# Archive System Integration Guide

This guide is for the private archive worker team integrating with the Osimi VPS backend.

It describes the worker-side protocol end to end: lease, download, event ingestion, heartbeat, and release.

For complete route reference, see `docs/api-reference.md` (Worker APIs section).

## 1) Scope and Boundaries

- This guide covers worker-to-VPS integration only.
- UI/client bearer endpoints are out of scope for worker implementations.
- Worker endpoints are outbound-only from the archive network to VPS.

## 2) Required Configuration (Worker Side)

- `VPS_BASE_URL` (for example `https://api.example.com`)
- `WORKER_AUTH_TOKEN` (must exactly match backend `WORKER_AUTH_TOKEN`)
- Optional `WORKER_ID` (sent as `x-worker-id` for observability)

Notes:

- The worker does not generate or verify lease signatures; it only forwards `lease_token` values returned by VPS.
- System clocks should be NTP-synchronized because lease/download tokens are expiry-based.

## 3) Authentication Model

### Header auth (all worker control endpoints)

- Header: `x-worker-auth-token: <WORKER_AUTH_TOKEN>`
- Optional header: `x-worker-id: <worker-id>`

Nuance:

- `x-worker-id` is metadata for observability. It is not currently enforced as an authorization binding.

### Lease auth (ingestion-bound endpoints)

- Endpoints that mutate lease state or ingest events require body field `lease_token`.
- `lease_token` is bound to:
  - `ingestion_id`
  - `tenant_id`
  - `lease_id`
  - token expiry
- If lease is expired/released/invalid, VPS rejects with non-2xx.

## 4) End-to-End Processing Flow

1. Call `POST /api/ingestions/lease`.
2. If `lease` is `null`, sleep and poll again.
3. If lease exists, download each file from `download_urls[]`.
4. Read and validate `lease.catalog_json` (schema: `docs/catalog_json.md`).
5. Verify checksums against your authoritative checksum source.
6. Process archive pipeline using `catalog_json` metadata and processing intent.
7. Send progress/outcome events via `POST /api/ingestions/:id/events`.
8. Send heartbeat periodically via `POST /api/ingestions/:id/lease/heartbeat` until done.
9. On graceful stop or abandonment, call `POST /api/ingestions/:id/lease/release`.

Recovery nuance:

- If your event notifier has pending events for a known ingestion but the token has expired, ask your lease-owning worker process to call `POST /api/ingestions/:id/lease` for deterministic reacquire.
- `POST /api/ingestions/:id/lease` does not steal active leases; it returns conflict when another active lease exists.

Catalog delivery contract:

- VPS provides `catalog.json` content in worker lease payload as `lease.catalog_json`.
- Worker should read metadata and processing intent from `lease.catalog_json`.
- `catalog_json` is human-owned metadata and can include human-entered fields and approved overrides.
- Catalog schema and validation rules are defined in `docs/catalog_json.md`.
- During ingestion-stage leasing, `catalog_json.object_id` may be `null` until object finalization.
- If catalog metadata is missing, the ingestion is not eligible for leasing.

Checksum contract (important):

- Lease payload currently includes `file_id`, `storage_key`, `content_type`, and `size_bytes`, but does not include checksum fields.
- Worker implementations must resolve expected checksums from their own authoritative ingestion context (for example, archive-side commit records).
- Treat checksum mismatch as processing failure and emit `FILE_FAILED` (or equivalent failure eventing used by your pipeline).

## 5) Endpoint Contracts

## 5.1 Lease next ingestion

`POST /api/ingestions/lease`

Headers:

- `x-worker-auth-token` (required)
- `x-worker-id` (optional)

Response `200`:

- No work:

```json
{
  "lease": null
}
```

- Work available:

```json
{
  "lease": {
    "lease_id": "uuid",
    "lease_token": "token",
    "lease_expires_at": "2026-02-19T18:00:00.000Z",
    "ingestion_id": "uuid",
    "batch_label": "batch-2026-02-19-001",
    "tenant_id": "uuid",
    "download_urls": [
      {
        "file_id": "uuid",
        "storage_key": "tenants/.../file.bin",
        "content_type": "application/octet-stream",
        "size_bytes": 12345,
        "checksum_sha256": "hex",
        "download_url": "/api/worker/downloads/<signed-token>"
      }
    ],
    "catalog_json": {
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
        "tags": [
          "source:family_archive"
        ],
        "summary": null
      },
      "dates": {
        "published": {
          "value": null,
          "approximate": true,
          "confidence": "low",
          "note": "Not yet identified"
        },
        "created": {
          "value": null,
          "approximate": true,
          "confidence": "low",
          "note": "Unknown"
        }
      }
    }
  }
}
```

Nuances:

- `download_url` is relative; prepend `VPS_BASE_URL`.
- `download_urls` includes files currently in `UPLOADED` or `VALIDATED` state.
- `checksum_sha256` is provided for worker-side integrity validation.
- `processing_overrides` is provided per file for pipeline override intent.
- `catalog_json` is included in lease payload; validate it against `docs/catalog_json.md` before processing.
- A redundancy sweep runs before lease assignment to recover expired processing leases.
- `download_urls` may be empty if no file is currently eligible for worker download.
- Recommended polling on `lease: null`: jittered backoff in the 2-10 second range.

## 5.2 Lease a specific ingestion (targeted recovery)

`POST /api/ingestions/:id/lease`

Headers:

- `x-worker-auth-token` (required)
- `x-worker-id` (optional)

Response `200`:

- Same lease payload shape as `POST /api/ingestions/lease` when work is available.

Failure semantics:

- `404` if ingestion id does not exist.
- `409` if ingestion exists but is not currently leasable (for example, active lease or non-`QUEUED` status).
- No force takeover is supported.

Use case:

- Deterministic recovery when your system has buffered events for a specific ingestion and needs a fresh lease token.

## 5.3 Heartbeat lease

`POST /api/ingestions/:id/lease/heartbeat`

Headers:

- `x-worker-auth-token` (required)
- `x-worker-id` (optional)

Body:

```json
{
  "lease_token": "token"
}
```

Response `200`:

- Returns refreshed `lease` including a new `lease_token`, new `lease_expires_at`, refreshed `download_urls`, and refreshed `catalog_json`.

Worker rule:

- Always replace your local token with the refreshed `lease_token` from heartbeat response.

## 5.4 Release lease

`POST /api/ingestions/:id/lease/release`

Headers:

- `x-worker-auth-token` (required)
- `x-worker-id` (optional)

Body:

```json
{
  "lease_token": "token"
}
```

Response `200`:

```json
{
  "status": "ok",
  "ingestion_id": "uuid",
  "lease_id": "uuid"
}
```

Behavior nuance:

- If ingestion is still `PROCESSING` when released, VPS re-queues it to `QUEUED`.

## 5.5 Download staged file

`GET /api/worker/downloads/:token`

Headers:

- No bearer header required.

Response `200`:

- Raw file bytes.
- Headers include `content-type`, `content-length`, `accept-ranges: bytes`.

Failure nuance:

- Signed token expiry or invalid signature returns non-2xx JSON error.
- Missing staged file returns `404`.

## 5.6 Post worker events

`POST /api/ingestions/:id/events`

Headers:

- `x-worker-auth-token` (required)
- `x-worker-id` (optional)

Body:

```json
{
  "lease_token": "token",
  "events": [
    {
      "event_id": "uuid",
      "event_type": "INGESTION_PROCESSING",
      "timestamp": "2026-02-19T18:10:00.000Z",
      "payload": {
        "step": "OCR"
      }
    }
  ]
}
```

Validation rules:

- `event_id` must be UUID.
- `timestamp` must be ISO-8601 datetime with offset.
- `payload` must be a JSON object.
- `object_id` format (when present): `OBJ-YYYYMMDD-XXXXXX`.
- `object_id` is required for:
  - `INGESTION_COMPLETED`
  - `OBJECT_CREATED`
  - `ARTIFACT_CREATED`
- `events` may be an empty array; VPS returns `200` with zero insert/duplicate counts.
- `event_id` must be globally unique across the worker fleet (not only per ingestion).

Response `200`:

```json
{
  "status": "ok",
  "ingestion_id": "uuid",
  "inserted_events": 1,
  "duplicate_events": 0,
  "object_id": "OBJ-20260219-ABC123"
}
```

## 6) Supported Event Types

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
- `OBJECT_CREATED`
- `ARTIFACT_CREATED`

## 7) Idempotency and Ordering Guarantees

- VPS deduplicates by `event_id`.
- Duplicate events are accepted and counted under `duplicate_events`.
- Out-of-order delivery is tolerated.

Worker guidance:

- Use stable UUIDs for retries of the same logical event.
- Retry on transient network failures with backoff.

## 8) State Side Effects (Current Phase)

### Ingestion status side effects

- `INGESTION_PROCESSING` drives ingestion status toward `PROCESSING`.
- `INGESTION_FAILED` drives ingestion status toward `FAILED`.
- `INGESTION_CANCELED` drives ingestion status toward `CANCELED`.
- `INGESTION_COMPLETED` drives ingestion status toward `COMPLETED`.
- Duplicate or out-of-order events are tolerated; VPS applies transition-safe updates.

### Object projection side effects

- On `INGESTION_COMPLETED`:
  - VPS creates or resolves object by source ingestion and `object_id`.
  - VPS updates object projection to:
    - `processing_state = index_done`
    - `availability_state = AVAILABLE`
- If `payload.ingest_json` is present and is a JSON object:
  - VPS stores it in `objects.ingest_manifest` (last-write-wins).
- `curation_state` is not currently projected from worker events.
- Other event types are persisted for audit/activity and do not directly mutate object projection fields.

## 9) Error Handling and Retry Policy

Use standard error envelope from `docs/api-reference.md`.

Practical handling:

- `401 UNAUTHORIZED`:
  - Missing/invalid `x-worker-auth-token`, invalid lease token, bad signature, expired token.
  - Action: fix credentials/token, do not blind retry.
- `409 CONFLICT`:
  - Lease no longer active, conflicting completion `object_id` for same ingestion.
  - Action: stop processing current lease context; reacquire via `/lease` if needed.
- `404 NOT_FOUND`:
  - Ingestion/file not found.
  - Action: stop current unit and surface alert.
- `400 BAD_REQUEST`:
  - Invalid payload shape/field values.
  - Action: fix worker serializer; do not blind retry.
- `5xx`:
  - Action: retry with exponential backoff and jitter.

Endpoint-specific handling:

- `POST /api/ingestions/:id/lease/heartbeat`:
  - `409` means lease is no longer active.
  - Action: stop heartbeat and stop processing this lease context; reacquire with `/api/ingestions/lease`.
- `POST /api/ingestions/:id/events`:
  - `409` means lease is no longer active or completion conflict.
  - Action: stop event posting for this ingestion and reacquire decision via new lease cycle.
- `POST /api/ingestions/:id/lease/release`:
  - `409` in `finally` cleanup can be treated as benign if lease already expired/released.
  - Action: log and continue.

## 10) Recommended Timing and Batching

- Lease TTL is currently 5 minutes.
- Send heartbeat every 60-120 seconds while processing.
- Batch events in small groups (for example 10-100) to reduce request overhead.
- Flush final outcome events before release.
- If a heartbeat attempt fails transiently, retry quickly once; if still failing near lease expiry, stop work and reacquire.

Release safety rule:

- Do not release lease before your terminal outcome event is accepted unless you intentionally abandon and requeue the ingestion.

## 11) Crash Recovery Expectations

- If worker crashes and does not release:
  - lease expires
  - ingestion becomes eligible for re-queue and future lease pickup
- Worker should be safe to restart and continue polling `/lease`.

## 12) Minimal Worker Loop (Pseudo)

```text
loop:
  lease = POST /api/ingestions/lease
  if lease == null:
    sleep(poll_interval)
    continue

  start heartbeat timer
  try:
    download all lease.download_urls
    process pipeline
    POST /api/ingestions/:id/events (progress + outcome)
  finally:
    stop heartbeat timer
    POST /api/ingestions/:id/lease/release (best effort)
```

## 13) Integration Checklist

- Worker sends `x-worker-auth-token` on all control/event requests.
- Worker stores and rotates latest `lease_token` after each heartbeat.
- Worker uses relative `download_url` with `VPS_BASE_URL`.
- Worker posts events with stable UUID `event_id` for retries.
- Worker includes `object_id` for completion/object/artifact events.
- Worker sends `ingest_json` in completion payload when available.
- Worker handles `401/404/409/5xx` with distinct actions.
- Worker releases lease on graceful stop.
- Worker ensures global uniqueness of `event_id` values.
- Worker uses jittered polling when `lease` is `null`.
- Worker reads `lease.catalog_json` on lease/heartbeat and validates against `docs/catalog_json.md`.
