# AGENTS.md

This file guides agentic coding tools working in this repo.
Keep changes small, follow existing patterns, and prefer standard Go tooling.

## Project snapshot

- Language: Go (module `github.com/ilova-bazis/osimi-archive`)
- Go version: `go 1.25.4` (from `go.mod`)
- Main binaries live under `cmd/`:
  - `cmd/ingestd` (ingest watcher + workers)
  - `cmd/orchestrator` (planner)
  - `cmd/pulld` (VPS puller -> ingest drop)
  - `cmd/workerd` (worker daemon)
- Storage: SQLite via `modernc.org/sqlite`
- Data layout: `data/osimi-archive` (see docs)

## Cursor / Copilot rules

- No Cursor rules found in `.cursor/rules/` or `.cursorrules`.
- No GitHub Copilot instructions found in `.github/copilot-instructions.md`.

## Build, run, lint, test

### Build binaries

```bash
go build ./cmd/ingestd
go build ./cmd/orchestrator
go build ./cmd/pulld
go build ./cmd/workerd
```

### Run binaries (local dev)

```bash
go run ./cmd/ingestd
go run ./cmd/orchestrator
go run ./cmd/pulld
go run ./cmd/workerd
```

Common env vars (see `internal/config/config.go`):

- `ARCHIVE_ROOT` (default `data/osimi-archive`)
- `INGEST_DROP` (default `data/osimi-archive/ingest_drop`)
- `DB_PATH` (default `data/osimi-archive/db/osimi.sqlite`)
- `POLL_INTERVAL` (default `3s`)
- `MAX_WORKERS` (default `4`)
- `DONE_MARKER` (default `DONE`)
- `VPS_BASE_URL` (required for `cmd/pulld`)
- `WORKER_AUTH_TOKEN` (required for `cmd/pulld`)
- `WORKER_ID` (optional)
- `LEASE_POLL_INTERVAL` (default `5s`)
- `LEASE_HEARTBEAT_INTERVAL` (default `90s`)

Example:

```bash
ARCHIVE_ROOT=data/osimi-archive INGEST_DROP=data/osimi-archive/ingest_drop \
DB_PATH=data/osimi-archive/db/osimi.sqlite POLL_INTERVAL=2s MAX_WORKERS=2 \
go run ./cmd/ingestd
```

### Tests

Run all tests:

```bash
go test ./...
```

Run tests for a single package:

```bash
go test ./internal/db
```

Run a single test by name (package-scoped):

```bash
go test ./internal/db -run TestName
```

Run a single test with verbose output:

```bash
go test ./internal/db -run TestName -v
```

### Linting / formatting

Formatting (standard Go format):

```bash
gofmt -w .
```

Static checks:

```bash
go vet ./...
```

SQL formatting (if needed) should follow `.sql-formatter.json`:

- language: SQLite
- keyword case: upper
- indent: 4 spaces
- keep existing line breaks when practical

## Code style guide

### Imports

- Group imports as: standard library, third-party, internal modules.
- Keep grouped imports separated by a single blank line.
- Use explicit aliases only when needed for clarity or conflicts.

Example:

```go
import (
    "context"
    "time"

    "github.com/google/uuid"

    "github.com/ilova-bazis/osimi-archive/internal/db"
)
```

### Formatting

- Run `gofmt` on all Go files.
- Keep line length reasonable; break long SQL or log strings across lines if needed.
- Use tabs per Go formatting norms.

### Types and structs

- Prefer explicit struct types over `map[string]any` when fields are known.
- Keep config and payload structs in the relevant package (`internal/config`, `internal/ingest`).
- Use `time.Time` and `time.Duration` for time values; avoid raw strings except at boundaries.

### Naming conventions

- Go idioms: `CamelCase` for exported, `camelCase` for unexported.
- Boolean fields/vars: `hasOCR`, `isReady`, `shouldEnqueue`.
- Error variables: `err` unless a more specific name clarifies.
- SQL tables/columns use snake_case as in `internal/db/schema.sql`.

### Error handling

- Return early on error; avoid deep nesting.
- Wrap with context using `%w` (example in `internal/db/db.go`).
- Log actionable context for long-running goroutines.
- For retry loops, log once per attempt and back off on failures.

### Context usage

- Accept `context.Context` in public-facing or IO-heavy functions.
- Use timeouts for DB queries (`context.WithTimeout`).
- Cancel goroutines on shutdown signals.

### Logging

- Use the standard library `log` package (current style).
- Log high-level lifecycle events at startup/shutdown.
- Prefer structured messages like `worker-%d: %v` for clarity.

### Database access

- Keep SQL in `internal/db` and favor prepared queries or parameterized statements.
- Use transactions where multiple statements must be atomic.
- Keep schema changes in `internal/db/schema.sql`.

### Filesystem + data model

- Filesystem is the source of truth (see `docs/system_design.md`).
- Marker files (e.g., `ocr/OCR_DONE`) indicate completion.
- Keep pipeline outputs deterministic and regenerable.

### Concurrency

- Use `sync.WaitGroup` for worker lifecycles and graceful shutdown.
- Use channels for signal handling and cancellation.
- Avoid unbounded goroutine spawning; reuse worker pools.

### Tests

- Name tests with `TestXxx` and table-driven patterns where possible.
- Keep tests in the same package unless you need black-box coverage.

## Docs and references

- Architecture: `docs/system_design.md`
- System architecture: `docs/architecture.md`
- Data layout: `docs/archived-item-structure.md`
- Catalog schema: `docs/catalog.json.md`
- Ingest schema: `docs/ingest.json.md`
- Worker API integration: `docs/backend-integration.md`

## Agent tips

- Do not commit local data under `data/`; it is ignored by `.gitignore`.
- Be careful with SQLite WAL files in `data/osimi-archive/db/`.
- When adding new pipeline steps, update planner logic before worker execution.
- Do not start planning or implementing integration logic without having a clear contract specification between the systems. If such contract is missing in the specifications and documents, insist on the contract to be provided.
