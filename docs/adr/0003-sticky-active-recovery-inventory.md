# ADR 0003: Sticky active-session recovery inventory

- Status: accepted

## Context

Niri observes windows, while Zellij remains authoritative for which persistent sessions are active. Treating a temporarily headless session as deleted loses its last useful workspace and layout evidence. Conversely, treating resurrection-cache entries or ambiguous catalog output as active can publish an unsafe recovery set.

## Decision

Schema 3 rolling checkpoints contain two distinct observations: normalized Niri state and an authoritative active Zellij allow-list with one recovery record per active name. Each record stores CWD, durable workspace reference, scrolling column and row, floating state, tile and window dimensions, placement observation time, and whether the session was visible in the current Niri capture.

Capture obtains the allow-list through `zellijlive.Cataloger`. Catalog failure, duplicate or inconsistent names, invalid sockets, and ambiguous statuses abort publication. Dead-resurrectable cache entries are not active. An exactly associated visible window refreshes placement. An active session without one retains its newest valid matching placement, first from the current boot and then from valid prior boots. A complete catalog omission removes the session from the current allow-list.

Schema 3 keeps the rolling one-file-per-boot design and its writer lock, temporary write, file fsync, atomic rename, and directory fsync. Its integrity hash binds semantic compositor state and the complete normalized recovery inventory. Schema 1 and 2 remain readable and may provide exact legacy visible placement; their event offset is migration-only and is never written into schema 3.

This inventory is checkpoint-owned. Mirror snapshots, projections, and pins neither store nor influence recovery carry-forward.

## Consequences

- Empty Niri state no longer erases active headless sessions or their last placement.
- The first capture after reboot can preserve placement while replacing the active allow-list with the new authoritative observation.
- Placement age remains explicit, so later recovery can distinguish fresh visibility from carried evidence.
- Row is persisted now; reconstructing stacked multi-window columns remains a later, explicitly degraded recovery concern.
