# Agent integration architecture

Filo is an independent service between remote clients and local agent harnesses.
The current implemented adapter is Codex Desktop on Windows. Other agent adapters
remain design work and must not be advertised as available control connections.

## Connection priority

1. Discover the original Desktop application, selected user and live IPC owner.
2. Verify actual protocol capabilities before dispatching an operation.
3. Use the unmodified runtime provided by Desktop only for capabilities unavailable
   through the original IPC interface. Never require a separately installed CLI.

Original active tasks remain with their existing owner. An independently started
process cannot control a task merely because it can read the same history or ID.
Discovery is not control authority, and shared files do not grant writer ownership.
Filo-created tasks use their own verified execution scope and independent lifetime.

## Ownership and failure

No desktop focus, keyboard, mouse or page automation is permitted. Filo never
patches Codex, redirects normal startup, rewrites native records, manufactures
approvals or signals the original Desktop/backend. Normal operation cannot depend
on a successful Filo connection.

Stopping the gateway or a Filo helper must not cancel accepted native generation.
An independent observer retains only the identity and lifetime resources needed
for a Filo-owned executor. It may retire that executor after independently verified
completion; it cannot terminate an original task or reuse a stale PID as authority.

Mutation failures do not authorize another writer, input replay or a replacement
execution path. Receipts represent native acceptance; actual completion and visible
history reconciliation require their own evidence.

## Protocol and data

The public listener uses the shared Conch authenticated encrypted transport.
Private adapter connections remain authenticated loopback endpoints. Bounded
history pages, separate image reads, bounded uploads and stream backpressure prevent
a large conversation or slow client from requiring an unbounded in-memory snapshot.

Native records are read-only. Rename and archive use native APIs; archive preserves
history. Images are authorized by native message identity and revision, not an
arbitrary client-supplied filesystem path.

The integration contracts are [Codex connection](codex-connection-contract.md),
[auxiliary execution](windows-auxiliary.md) and
[encrypted public API](encrypted-public-api.md). Historical feasibility probes and
machine-specific execution evidence are retained privately and are not product
capability or compatibility guarantees.
