# Original desktop attachment

Read [the connection contract](codex-connection-contract.md) and
[the auxiliary boundary](windows-auxiliary.md) before integration work.

## Existing desktop tasks

`DesktopIpc` opens a disposable connection to the original application's IPC router.
Application identity, account and interactive session are verified before connection.
`DesktopSessions` discovers the task owner and follows its native state. Existing idle
tasks start on that owner; active tasks steer or stop their exact original turn.
Settings updates also go to that owner. Dispatch rechecks owner identity.

Neither missing ownership nor a failed operation grants permission to resume through
an auxiliary process. Reading history never acquires a competing execution owner.
An unavailable or oversized native snapshot can use the bounded persisted history
projection, but that projection does not establish live execution authority.

Closing Filo closes its own follower connection. It never stops the original desktop
or backend, redirects normal startup, or modifies native application files or history.
The withdrawn global shared-backend startup integration is prohibited.

## Peripheral native operations

The selected original desktop's bundled or managed runtime supplies auxiliary native
APIs. Installation does not require a separate CLI or a PATH `codex` command. Resolve
the runtime dynamically; a tested version or native hash is diagnostic information,
not a production allowlist.

Auxiliary metadata operations and original desktop execution have separate lifetimes.
A successful native `thread/start` response alone does not prove that a new task is
durable, resumable, or ready for a first turn in the original desktop. The current
create-to-original-owner transition remains unqualified. Do not present the isolated
shared-host test as proof that this transition works.

## Verification

Test ownership, cancellation and helper lifetime independently. A disconnected
client or successful unsubscribe does not prove that a native writer was released.
Do not create placeholder turns or replay input to manufacture durable history.
Use dedicated test tasks and verify ordinary desktop tasks remain unaffected.

Go protocol fixtures exercise frames, RPC, streaming and session behavior without
consuming model usage. They do not establish real desktop integration or installed
failure isolation. See the [connection contract](codex-connection-contract.md) for
the required integration checks.
