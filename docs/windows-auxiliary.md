# Windows auxiliary boundary

Agora connects to the independent SYSTEM gateway. The original desktop retains
its normal startup backend and owns existing active tasks. The authenticated user
helper connects to a separately supervised, unchanged runtime bundled with the
selected Codex Desktop. No separate CLI installation is required.
The current [connection contract](codex-connection-contract.md) requires IPC first,
forbids desktop control, and permits an additional native process only for a proven
IPC capability gap. A missing owner or failed request alone cannot trigger another
writer. An explicit Send/settings request may recover an existing thread only after
proving it is idle under the selected account and rechecking absent IPC ownership;
native resume must still acquire its writer lock without takeover. GET, catalog,
Attach and SSE remain read-only and never allocate an executor.

The helper's `NativeMetadata` component provides the model catalog, rename and
native archive. It has no create, resume, send or stop interface. The withdrawn
`sessions.json` registry is neither loaded nor rewritten, so a corrupt leftover
cannot prevent helper startup. Native metadata writes are never replayed.

New tasks use `TaskExecutorFactory` and a private executor for their exact native
identity. Creation provenance is recorded only after `thread/start` acknowledges
that identity. Explicit idle recovery has its own exact-thread admission; ordinary
history presence grants no writer authority. Browsing cannot admit a task or allocate
a writer. An original desktop owner takes precedence over every auxiliary scope.
On Windows, an openable process handle alone does not prove a live native writer:
exited processes may retain their handles. Replacement still requires exact-task
idle admission, an exited published writer and no live guardian; denied process
access or unreadable exit status cannot establish that the writer ended.

`NativeTaskSession` handles only that admitted executor's creation, settings,
runtime metadata, stop and archive. `NativeHistory` is the sole persisted-history
reader; `TaskActivity` retains bounded public deltas while history catches up.
There is no generic second history cursor, queue reader or unbounded live cache.
Once native state reports no active turn, persisted message bodies supersede stale
partial deltas. The first indexed page retains a terminal native error even when
its cursor is reached through the rollout/index boundary.

Both catalog metadata and native execution state are required for a complete new-task
flow. Creating an empty catalog entry alone does not prove first-message delivery.
The public gateway routes existing live tasks to their original desktop owner and
Filo-created and explicitly recovered idle tasks to their authenticated private executor. The unchanged
native runtime retains accepted execution independently of the gateway. A keeper and
independent GUI lifetime observer release only that owned native process after verified
completion. Missing lifetime protection fences new input. Isolated native tests do
not establish complete service/update/uninstall qualification or installed delivery.

Helper shutdown first fences pending mutations and verifies the separate metadata
host's idleness. A rejected preflight restores request admission. Accepted tasks on
private executors may continue after the helper closes its client connections.
Stopping a scheduler entry does not necessarily stop its detached user gateway;
installer shutdown uses the authenticated helper endpoint and verifies completion.
No original desktop process is signaled.

Client disconnection is not native writer release: actual isolated probes still
observed a writer conflict after unsubscribe and WebSocket close while the auxiliary
host remained alive. Only exit of that owned host released the persisted fixture.
An empty task may have no resumable rollout even after rename; verify its loaded
identity with `thread/read` before first input. Native archive can move its history
file: verify the current path through the native API rather than treating the old
location as a deletion. Keep independent completion and exact-task recovery in the
acceptance matrix. See [native attachment evidence](original-desktop-attachment.md).

`scripts/windows/Run-Standalone.ps1` and the `standalone` entry point of the installed
Filo binary keep their registered launcher and argument shape so an existing task
registration stays valid. They are current auxiliary launchers, not a desktop backend
redirect or an alternate public Agora connection. Their old history-execution
implementation has been removed. Historical deployment instructions and evidence
remain available in Git history.
