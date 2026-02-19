# `meta/pipeline.json` State (v1)

This document defines the **system-owned pipeline state** stored in `meta/pipeline.json`.

`pipeline.json` records which processing steps have run, with parameters and outputs.
It is **not authoritative** and can be rebuilt from filesystem evidence.

---

## Location

Within an archived item directory:

```
OBJ-YYYYMMDD-XXXXXX/
  meta/
    pipeline.json
```

---

## Design goals

- **System-owned**: never edited by humans
- **Rebuildable**: derived from outputs + markers
- **Lightweight**: minimal status metadata for orchestration
- **Versioned**: can evolve without breaking older readers

---

## Top-level fields (v1)

| Field | Type | Description |
|---|---|---|
| `schema_version` | string | Schema version, e.g. `"1.0"` |
| `object_id` | string | Object ID, must match folder name |
| `updated_at` | string | RFC3339 UTC timestamp of last update |
| `tasks` | object | Per-task status and parameters |

---

## `tasks` object

Each task key stores status, parameters, and outputs.

Example keys:
- `ocr_text`
- `audio_transcript`
- `derivatives`
- `index`

### Task shape (v1)

| Field | Type | Description |
|---|---|---|
| `status` | string | `queued` \| `running` \| `done` \| `failed` |
| `updated_at` | string | RFC3339 UTC timestamp |
| `params` | object | Task parameters (language, engine, version) |
| `outputs` | object | Output paths or references |
| `error` | string \| null | Error message on failure |

---

## Notes

- Marker files remain valid completion signals and can coexist with `pipeline.json`.
- `pipeline.json` is a convenience for orchestration and observability only.
- If missing or corrupt, it should be recreated from outputs and markers.
