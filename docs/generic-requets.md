# Archive Generic Requests Worker Guide

This guide explains the generic archive request queue used for worker-driven archive actions.

It is intended for archive-system developers implementing worker loops and handlers.

For full HTTP contracts, see `docs/api-reference.md` (`## Worker APIs`).

## 1) Why this exists

- We now support archive-facing operations through one generic queue model.
- Instead of creating a new queue table and lease API for every feature, workers consume a single archive request protocol.
- Current production action:
  - `object_resync`
- Reserved for next migration:
  - `artifact_fetch`

## 2) Concepts

- `archive_request`:
  - one queued unit of archive work
- `target_type`:
  - what entity this work applies to (`object|ingestion`)
- `target_id`:
  - concrete target id (for `object`, this is `OBJ-...`)
- `action_type`:
  - what to do (`object_resync|artifact_fetch`)
- `action_payload`:
  - action-specific JSON object
- `lease_token`:
  - short-lived signed token proving active lease ownership

## 3) Data model (worker-relevant fields)

Each request includes:

- identity: `id`, `tenant_id`
- routing: `target_type`, `target_id`, `action_type`, `action_payload`
- requester metadata: `requested_by`, `dedupe_key`
- status: `PENDING|PROCESSING|COMPLETED|FAILED|CANCELED`
- failure: `failure_reason`, `failure_details`
- timestamps: `created_at`, `updated_at`, `completed_at`

Worker lease internals (`lease_id`, `lease_token_id`, `lease_expires_at`, `leased_by`, `released_at`) are backend-managed and not a worker contract except via lease response payload.

## 4) Authentication and lease rules

- All archive-request worker endpoints require:
  - `x-worker-auth-token: <WORKER_AUTH_TOKEN>`
- Optional:
  - `x-worker-id: <worker-id>`
- Lease-protected endpoints additionally require body field:
  - `lease_token`

Tenant rule (important):

- Archive workers must not provide `tenant_id` in request bodies.
- Backend resolves tenant context from request ownership and lease token context.
- `tenant_id` in lease responses is informational only for logs/observability.

Important:

- Worker does not sign or verify lease tokens. It stores and forwards them.
- Every successful heartbeat returns a refreshed token. Replace local token immediately.

## 5) Endpoints and flow

## 5.1 Lease next request

`POST /api/archive-requests/lease`

Body (optional):

```json
{
  "action_type": "object_resync"
}
```

Notes:

- Body may be omitted (`{}` is valid).
- If `action_type` is provided, leasing is filtered to that action only.
- Lease TTL is 5 minutes.
- Backend sweeps expired leases before selecting new work.
- `tenant_id` is not accepted as input.

Response (`200`):

```json
{
  "request": {
    "request_id": "uuid",
    "lease_id": "uuid",
    "lease_token": "token",
    "lease_expires_at": "2026-03-09T12:00:00.000Z",
    "tenant_id": "uuid",
    "target_type": "object",
    "target_id": "OBJ-20260306-RSYNC1",
    "action_type": "object_resync",
    "action_payload": {},
    "requested_by": "uuid",
    "dedupe_key": "object_resync:OBJ-20260306-RSYNC1"
  }
}
```

No work:

```json
{
  "request": null
}
```

## 5.2 Heartbeat lease

`POST /api/archive-requests/:id/lease/heartbeat`

Body:

```json
{
  "lease_token": "token"
}
```

- `tenant_id` is not accepted as input.

Response (`200`):

```json
{
  "request": {
    "request_id": "uuid",
    "lease_id": "uuid",
    "lease_token": "new-token",
    "lease_expires_at": "2026-03-09T12:05:00.000Z"
  }
}
```

Rule: replace local token with the new `lease_token`.

## 5.3 Release lease (requeue)

`POST /api/archive-requests/:id/lease/release`

Body:

```json
{
  "lease_token": "token"
}
```

- `tenant_id` is not accepted as input.

Response (`200`):

```json
{
  "status": "ok",
  "request_id": "uuid"
}
```

Behavior:

- request transitions from `PROCESSING` back to `PENDING`
- use for graceful shutdown or temporary abandonment

## 5.4 Complete request

`POST /api/archive-requests/:id/complete`

Body:

```json
{
  "lease_token": "token"
}
```

- `tenant_id` is not accepted as input.

Response (`200`):

```json
{
  "status": "completed",
  "request": {
    "id": "uuid",
    "tenant_id": "uuid",
    "target_type": "object",
    "target_id": "OBJ-20260306-RSYNC1",
    "action_type": "object_resync",
    "action_payload": {},
    "requested_by": "uuid",
    "dedupe_key": "object_resync:OBJ-20260306-RSYNC1",
    "status": "COMPLETED",
    "failure_reason": null,
    "failure_details": null,
    "created_at": "2026-03-09T11:55:00.000Z",
    "updated_at": "2026-03-09T12:01:00.000Z",
    "completed_at": "2026-03-09T12:01:00.000Z"
  }
}
```

## 5.5 Fail request

`POST /api/archive-requests/:id/fail`

Body:

```json
{
  "lease_token": "token",
  "failure": {
    "code": "SYNC_FAILED",
    "message": "Archive source unavailable",
    "retryable": true,
    "details": {
      "upstream_status": 503
    }
  }
}
```

- `tenant_id` is not accepted as input.

Response (`200`):

```json
{
  "status": "failed",
  "request_id": "uuid",
  "retryable": true
}
```

Behavior:

- backend stores `failure_reason` from `failure.message`
- backend stores `failure_details` object
- if `failure.details` is omitted, backend stores `{}`

## 6) Current action contract: `object_resync`

Current producer endpoint:

- `POST /api/objects/:object_id/resync`

Request creation behavior:

- If no active request exists for the object, backend creates one and returns `201`.
- If active `PENDING|PROCESSING` request exists, backend dedupes and returns existing one with `200`.

Worker handling expectation:

- filter leasing with `{"action_type": "object_resync"}`
- when request leased:
  - call archive system to refresh available-file snapshot for `target_id`
  - write snapshot via existing endpoint:
    - `PUT /api/internal/objects/:object_id/available-files`
  - if refresh succeeded, call `/complete`
  - if refresh failed, call `/fail`

## 7) Status machine

Expected transitions:

- `PENDING -> PROCESSING` (lease)
- `PROCESSING -> PROCESSING` (heartbeat)
- `PROCESSING -> PENDING` (release or lease expiry sweep)
- `PROCESSING -> COMPLETED` (complete)
- `PROCESSING -> FAILED` (fail)

## 8) Error handling and retries

General handling:

- `400 BAD_REQUEST`:
  - invalid body/path
  - fix serializer/request builder; do not blind retry
- `401 UNAUTHORIZED`:
  - missing/invalid worker auth token, invalid/expired lease token
  - fix credentials/token handling; do not blind retry
- `409 CONFLICT`:
  - lease no longer active (expired/released/stolen by timeout recovery)
  - stop work for that lease context; reacquire with `/lease`
- `500 CONFIGURATION_ERROR`:
  - backend worker auth not configured
  - operational fix required
- other `5xx`:
  - retry with exponential backoff + jitter

Failure ownership rule:

- If archive work is definitively failed for this request, call `/fail`.
- If worker is shutting down or temporarily unable to continue, call `/lease/release`.

## 9) Polling and heartbeat recommendations

- Poll interval when no work: jittered 2-10 seconds.
- Heartbeat interval while processing: 60-120 seconds.
- Always maintain local deadline from `lease_expires_at` and refresh before expiry.

## 10) Minimal worker loop (pseudo)

```text
loop:
  lease = POST /api/archive-requests/lease { action_type: "object_resync" }
  if lease.request == null:
    sleep(jittered_poll_interval)
    continue

  request = lease.request
  token = request.lease_token
  start heartbeat timer

  try:
    if request.action_type == "object_resync":
      refresh archive metadata for request.target_id
      PUT /api/internal/objects/:object_id/available-files
      POST /api/archive-requests/:id/complete { lease_token: token }
    else:
      POST /api/archive-requests/:id/fail { lease_token: token, failure: unsupported_action }
  catch err:
    POST /api/archive-requests/:id/fail { lease_token: token, failure: mapped_error }
  finally:
    stop heartbeat timer
```

## 11) Implementation checklist for archive team

- Include `x-worker-auth-token` on all archive-request control calls.
- Optionally include stable `x-worker-id` for observability.
- Store latest `lease_token` and rotate it after heartbeat.
- Branch handler by `action_type`.
- For `object_resync`, use `target_id` as canonical object id.
- Use `/complete` on success, `/fail` on business failure, `/lease/release` on graceful abandonment.
- Treat `409` as lease loss and stop current lease context.
- Retry transient network/server failures with jittered backoff.

## 12) Relationship to other worker guides

- Ingestion pipeline workflow remains in `docs/archive-system-integration.md`.
- Available-files snapshot semantics are in `docs/archive-system-available-files-sync.md`.
- Generic queue/lease API contract is in this document.
