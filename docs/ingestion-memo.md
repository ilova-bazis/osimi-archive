# Archive-System Memo: New Ingestion Batch Contract

Audience: archive-system worker team.

This memo describes the updated ingestion lease/event contract to support:

- one ingestion -> many logical items
- ordered files within each item
- item-level completion/failure signaling

---

## 1) Conceptual Model

Backend ingestion payloads now represent:

```text
ingestion (submission envelope)
  -> ingestion items (logical future object boundaries)
    -> files (ordered representations)
```

Key point: a single ingestion may include multiple item groups.

---

## 2) Lease Payload Changes

Endpoints:

- `POST /api/ingestions/lease`
- `POST /api/ingestions/:id/lease`
- `POST /api/ingestions/:id/lease/heartbeat`

`lease.items[]` is now the authoritative batch payload.

Each `items[]` entry includes:

- `ingestion_item_id`
- `item_index`
- `catalog_json` (item-scoped, authoritative for future object)
- `files[]`

Each `files[]` entry includes:

- `file_id`
- `filename`
- `sort_order`
- `storage_key`
- `content_type`
- `size_bytes`
- `checksum_sha256`
- `processing_overrides`
- `download_url`

Ordering guarantee:

1. items ordered by `item_index ASC`
2. files inside each item ordered by `sort_order ASC`, then `filename`, then `file_id`

Notes:

- only files in `UPLOADED|VALIDATED` are included
- all committed files must be linked to ingestion items before lease

---

## 3) Worker-Side Grouping/Ordering Expectations

Expected worker behavior:

- iterate `lease.items[]`
- for each item, use `item.catalog_json` as object metadata source
- process `item.files[]` in order

Recommended local disk layout:

- `item_001/file_0001-original.ext`
- `item_001/file_0002-original.ext`

Current backend behavior:

- backend provides item-scoped metadata and file ordering in lease
- backend does not currently rename staging files by `sort_order`
- worker should apply final naming convention when persisting locally

---

## 4) Event Contract Changes

Endpoint:

- `POST /api/ingestions/:id/events`

New supported event types:

- `INGESTION_ITEM_CREATED`
- `INGESTION_ITEM_UPDATED`
- `INGESTION_ITEM_PROCESSING`
- `INGESTION_ITEM_COMPLETED`
- `INGESTION_ITEM_FAILED`

Validation requirements:

- `ingestion_item_id` is required for `INGESTION_ITEM_*` events
- `object_id` is required for:
	- `INGESTION_ITEM_COMPLETED`
	- `OBJECT_CREATED`
	- `ARTIFACT_CREATED`

Idempotency:

- still deduped by `event_id`

---

## 5) Completion Semantics

Item-level completion/failure updates `ingestion_items` state.

Ingestion aggregate status is derived from item outcomes:

- `COMPLETED` when all items complete successfully
- `COMPLETED_WITH_ERRORS` when mixed success/failure/skip exists
- `FAILED` when all terminal and no useful success

Legacy ingestion-level completion events continue to work.

---

## 6) Example Lease (trimmed)

```json
{
  "lease": {
    "ingestion_id": "<ingestion-uuid>",
    "items": [
      {
        "ingestion_item_id": "<item-uuid-a>",
        "item_index": 1,
        "catalog_json": {
          "schema_version": "1.0",
          "object_id": null
        },
        "files": [
          {
            "file_id": "<file-uuid-1>",
            "filename": "page-001.tif",
            "sort_order": 1,
            "download_url": "/api/worker/downloads/<token>",
            "checksum_sha256": "..."
          },
          {
            "file_id": "<file-uuid-2>",
            "filename": "page-002.tif",
            "sort_order": 2,
            "download_url": "/api/worker/downloads/<token>",
            "checksum_sha256": "..."
          }
        ]
      }
    ]
  }
}
```

---

## 7) Example Item Completion Event

```json
{
  "lease_token": "<lease-token>",
  "events": [
    {
      "event_id": "<uuid>",
      "event_type": "INGESTION_ITEM_COMPLETED",
      "timestamp": "2026-03-16T12:00:00.000Z",
      "ingestion_item_id": "<item-uuid-a>",
      "object_id": "OBJ-20260316-ABC123",
      "payload": {
        "ingest_json": {
          "schema_version": "1.0"
        }
      }
    }
  ]
}
```

---

## 8) Migration Guidance for Worker Team

1. Parse and store `lease.items[]` and `item.catalog_json`.
2. Switch processing planner from flat file list to item-group aware batches.
3. Emit `INGESTION_ITEM_*` events as primary progress channel.
4. Keep existing ingestion-level completion events during transition for compatibility.
