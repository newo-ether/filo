# Filo Windows SYSTEM gateway

Agora connects to one independent LocalSystem gateway through the shared Conch
authenticated encryption channel. One server owns the payload cache, event hub,
image-reader admission and upload budget for every configured source. Additional
sources never create additional public servers or budgets.

## Selected sources and identity

Both service and private worker configuration use `sources`, an ordered array of
one to three unique IDs: `codex-desktop`, `codex-cli`, `claude-code`. An absent field
preserves Desktop-only behavior. An explicit empty, duplicate or unknown source is
rejected. `desktopSid` is the selected ordinary Windows account for all sources;
the existing key name is retained for compatibility. A CLI/Claude worker verifies
its current process SID before any native source connection and refuses SYSTEM or
a different account. Public LocalSystem code never opens those native sources.

Only a selected Desktop source creates TaskSessions, a task executor directory,
an upload store, Desktop IPC client or auxiliary app-server connection. Without
Desktop the gateway still serves authenticated source descriptors, scoped history
and safe shutdown. It does not pretend to provide a legacy Codex surface; old
clients must use a compatible source-aware version.

Desktop and CLI derive opaque account/store scope from the same private worker
key, selected SID, verified native catalog file identity and fresh authentication
digest. Their canonical session reference is SHA-256 of JSON
`["codex",nativeScopeId,nativeSessionId]`, encoded as raw URL-safe base64. Neither
scope nor canonical identity grants control. Account or store replacement must
invalidate old pages, cursors, subscriptions and mutation bindings. Read-only row
projection can reuse one captured scope within a request, provided the current
scope is checked before publishing; native dispatch always verifies fresh scope.

## Codex Desktop

The gateway discovers registered original Codex Desktop and verifies the selected
account and actual task owner before live start, steer, stop or settings operations.
Closing Filo releases its own sockets and followers; it never terminates Desktop.
Catalog/history remain bounded read-only projections. Catalog identity lookup does
not acquire followers, discover an executor or subscribe all rows to live state.

The private helper supplies peripheral native API operations and verified created
or dormant-task control. It runs as the ordinary user with an unchanged Desktop
runtime. Scoped mutations carry the exact native `accessBinding` in the encrypted
JSON body, not an arbitrary transport header. The original native scope and owner
binding must still match at final dispatch. Unscoped legacy Desktop behavior is
preserved while scoped control requires that proof. Missing helpers or ambiguous
ownership do not authorize replay, writer takeover or an alternate executor.

## Codex CLI observation

CLI uses only the selected user's native history owner and fresh account/store
scope. Optional `codexDaemonSocket` must be an explicit bounded absolute local
socket path. No daemon is started and no app-server is substituted. Optional native
metadata requires kernel peer identity and verified actual catalog/auth identity;
a matching reported path is insufficient. Until that proof is available, models
and usage capabilities remain false. Send, Stop, settings, creation, rename,
archive and uploads are not advertised by this read-only source.

## Claude Code Mod observation

`claudeBridgePort` defaults to 7438 and must differ from private worker port 7437
and any configured worker port. Its listener binds only `127.0.0.1`, with bounded
header/body/write/idle timeouts. Only `/claude-mod/v1/` is handled by the official
Mod bridge. This listener is not mounted on the public service.

The explicit Mod environment is one `FILO_CLAUDE_CONFIG` JSON value:

```json
{"url":"http://127.0.0.1:7438/claude-mod/v1/","tokenFile":"C:\\FiloWorker\\claude-mod-token"}
```

`tokenFile` is absolute and refers to a distinct 256-bit secret inside the existing
private worker directory. An absent file is generated exclusively; an invalid or
reused worker token is refused instead of overwritten. The public service token
never authenticates the bridge. Claude has an installation-local isolated scope,
no claimed Codex account continuity and no execution authority. Observation is
bounded from registration, with native IDs/turns preserved. Filo does not rewrite
Claude startup, authentication, history or transcripts. Stopping the helper closes
only Filo's bridge/client transport and does not cancel native generation.

## Lifecycle and verification

Desktop absence never prevents authenticated gateway info. Missing helper
credentials fail the affected operation and can recover on a later request.
Failure/update/uninstall must leave original clients and accepted native turns
independent. Before retiring an auxiliary, installation checks its Filo task,
process ancestry, selected account, native idle state and PID identity. Updates
preserve tokens and roll back prior Filo configuration and registrations.

Go fixtures prove bounded assembly, encrypted entry, source isolation and failure
fences. They do not establish real native continuity, ordinary-user installation,
update/uninstall safety or owner mobile acceptance. Qualify those separately using
[the connection contract](codex-connection-contract.md) and
[installer guide](windows-installer.md).
