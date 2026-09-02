---
title: Collapse checkpoints to one unversioned recovery format
priority: high
---

## Goal

Remove checkpoint schema versioning and legacy migration paths so capture, resume, doctor, and tests operate on one current recovery checkpoint shape.

## Acceptance Criteria

- Checkpoint JSON and Go types have no schema version or legacy event-offset fields; new writes use one unversioned shape.
- All v1/v2/v3 conditional validation, title-hash compatibility, conversion, planner gating, doctor schema counters, and legacy-only tests/documentation are deleted rather than replaced with another compatibility abstraction.
- Recovery inventory validation, full integrity hashing, crash-durable atomic publication, sticky placement, resume safety, pruning, and locking remain intact.
- Existing versioned live files are not modified or deleted by implementation; documentation states they must be cleared separately before first deployment capture.
- `go test ./...`, `go vet ./...`, and `nix flake check` pass, with an independent review confirming no hidden version branching remains.

## Design Decisions

- Hard cut: no backward compatibility or migration framework.
- Keep one checkpoint shape without a version marker.
- Do not touch live state as part of source implementation.

## Implementation Notes

Search all SchemaVersion, checkpoint `.V`, schema-number, EventOffset, legacy title-hash, and v1/v2 conversion references. Mirror pin schema is separate and out of scope unless it accidentally shares checkpoint machinery.
