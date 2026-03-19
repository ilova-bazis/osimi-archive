# Pulld Test Execution Plan

This document tracks the test implementation roadmap for `cmd/pulld` so we can execute in small, reliable increments and avoid losing context.

## Progress

- Phase 0 status: completed
- Phase 1 Section A status: completed
- Phase 1 Section B status: completed
- Phase 1 Section C status: completed
- Phase 3 status: completed for archive/download worker lease paths
- Phase 4 Section A status: completed
- Phase 4 Section B status: completed
- Phase 4 Section C status: completed
- Phase 4 Section D status: completed
- Phase 5 status: completed
- Phase 6 status: in progress (tests added; local Go linker panic blocking full verification)
- Phase 7 status: completed (CI workflow added for `go test ./...` and `go test -race ./...`)
- Current focus: Phase 6 verification and stabilization

## Goal

- Add focused, deterministic tests around worker flows and lease/error handling.
- Protect the new generic archive-request (`object_resync`) integration.
- Improve confidence in retries, state transitions, and checksum validation.

## Execution Order

1. Phase 0 - Test foundation
2. Phase 1 - Archive requests worker (highest priority)
3. Phase 2 - Download requests worker
4. Phase 3 - Cross-worker lease-loss (`409`) consistency
5. Phase 4 - Ingestion lease worker (pull/drop + checksums)
6. Phase 5 - Backend object task worker
7. Phase 6 - Notifier/event worker
8. Phase 7 - CI quality gates

## Phase 0 - Test Foundation

### Section A: Scaffolding

- Add initial `*_test.go` files under `cmd/pulld`.
- Create reusable helpers (temp dirs, fixture writers, checksum helpers).

### Section B: Testability Seams

- Introduce narrow interfaces for dependencies used by worker handlers instead of concrete `*db.DB` and `*vps.Client` coupling.
- Define handler-scoped interfaces (archive requests, download requests, ingestion lease, backend object tasks, notifier/events) so tests can use minimal fakes.
- Add injectables for loop timing behavior:
  - sleep function
  - jitter function
  - ticker creation (or a loop-step helper)
- Keep default production wiring unchanged and centralize injectables in one place.

### Section C: Assertions

- Add helper assertions for common HTTP error outcomes (`409`, `401`, etc.).
- Replace ad-hoc string status matching with one shared status classifier helper used by all workers.

### Section D: Deliverables

- Add one smoke test per new seam/hook to prove deterministic control in tests.
- Document seam names in test files so later phases reuse the same harness.

### Acceptance

- Test harness compiles and at least one smoke test passes.
- No runtime behavior changes; only testability seams and helpers.

## Phase 1 - Archive Requests Worker

Worker: generic archive request loop (`object_resync`)

### Section A: `processArchiveRequest` Table Tests

- Success path: lease -> snapshot build -> `PutAvailableFiles` -> `CompleteArchiveRequest`.
- Unsupported action -> `FailArchiveRequest` with `UNSUPPORTED_ACTION` (`retryable=false`).
- Missing `request_id` or `target_id` validation errors.
- `GetObjectRoot` failure -> `OBJECT_NOT_FOUND` (`retryable=false`).
- Snapshot build failure -> `SNAPSHOT_BUILD_FAILED` (`retryable=true`).
- Snapshot sync failure -> `SNAPSHOT_SYNC_FAILED` (`retryable=true`).
- Complete conflict (`409`) handled as lease loss.

### Section B: Loop Behavior (Deterministic)

- No work path sleeps with jitter (2-10s).
- Lease errors sleep using fallback poll interval.
- Context cancellation exits loop quickly.

### Section C: Lease Lifecycle Tests

- Release is called on non-complete exits.
- Release is not called when completion succeeds.
- Heartbeat token rotation uses newest `lease_token`.

### Acceptance

- Core archive-request flow fully covered.

## Phase 2 - Download Requests Worker

Worker: object-download request flow

### Section A: `processDownloadRequest` Matrix

- Success with presign/upload/complete.
- Artifact resolution errors map to expected fail codes.
- File stat/open failures produce non-retryable failures.
- Presign size mismatch path is covered.
- Upload and complete failures are covered.

### Section B: Lease and Heartbeat

- Heartbeat updates token.
- Release-on-exit behavior is covered.
- Lease-loss (`409`) handling is consistent.

### Section C: Resolver Coverage

- `resolveArtifactPathByKind` edge cases.
- `resolveArtifactPathFromKey` format and fallback behavior.
- Deterministic file candidate selection.

### Acceptance

- Download worker failure classes are covered by deterministic tests.

## Phase 3 - Cross-Worker Lease-Loss (`409`) Consistency

Workers: archive requests, download requests, and notifier/event delivery paths that operate under leases

### Section A: Shared Behavior Rules

- Define explicit expected behavior when lease is lost (`409`):
  - stop processing current lease context
  - do not double-report terminal failure
  - avoid noisy repeated release/fail attempts
- Ensure behavior is consistent across all worker flows.

### Section B: Status Classification

- Verify each worker path uses the shared status classifier from Phase 0.
- Add tests that fail if behavior regresses back to fragile string matching patterns.

### Acceptance

- Lease-loss handling is deterministic and consistent across workers.

## Phase 4 - Ingestion Lease Worker

Worker: ingestion lease processing (`handleLease`, `downloadBatch`)

### Section A: Per-File URL Checksum Strict Mode (`downloadBatch`)

- Missing `checksum_sha256` fails.
- Invalid checksum format fails.
- Checksum mismatch fails.
- Valid checksum succeeds.

### Section B: Optional Checksum Manifest Path (`handleLease`)

- Valid checksum manifest parses and verifies against downloaded files.
- Malformed checksum line fails with parse error.
- Missing manifest checksum entry for a file fails.
- Manifest checksum mismatch fails.

### Section C: Drop Lifecycle

- Temp/final directory flow.
- Existing `DONE` behavior.
- Existing `DONE` + `ERROR` behavior.

### Section D: Event Side Effects

- Start/complete/failure event enqueue behavior for major branches.

### Acceptance

- Strict checksum contract is enforced by tests.

## Phase 5 - Backend Object Task Worker

Worker: backend object task delivery (`available_files_snapshot`)

### Section A: `deliverBackendObjectTask`

- Supported action success path.
- Unsupported action failure path.
- Correct DB updates (`sent` vs `failed`).

### Section B: Snapshot Builder

- Artifact detection by kind/variant.
- Include/exclude originals behavior.
- Sorting and metadata shape checks.

### Acceptance

- Snapshot publishing path is stable and covered.

## Phase 6 - Notifier/Event Worker

Worker: event delivery (`deliverEvent`)

### Section A: Lease Refresh

- Valid lease uses current token.
- Expired lease triggers targeted reacquire.

### Section B: Delivery Outcomes

- Success marks event as sent.
- Failure marks event failed with retry schedule.

### Section C: Payload/Object ID Handling

- Malformed payload handling.
- Object ID pass-through behavior validation.

### Acceptance

- Event delivery and retry behavior are covered.

## Phase 7 - CI and Quality Gates

- Run `go test ./...` in CI.
- Add race test pass (`go test -race ./...`) or scoped race run if needed for runtime.
- Track initial coverage baseline for `cmd/pulld` and set incremental target.

## Notes

- Keep each phase small and mergeable.
- Prefer table-driven tests where practical.
- Reuse fixtures/helpers; avoid duplication between worker test suites.
- Prioritize low-refactor, high-signal tests first in each phase.
