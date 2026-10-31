# Filo Windows SYSTEM gateway

Agora connects to one independent LocalSystem gateway through the shared Conch
authenticated encryption channel. One server owns the payload cache, event hub,
image-reader admission and upload budget for every configured source. Additional
sources never create additional public servers or budgets.

Installed executables use `C:\Program Files\Filo\bin`, registered once in machine
PATH. Service configuration, tokens and logs use `C:\ProgramData\Filo\service`;
selected-account state and its fixed native runtime copy use
`C:\ProgramData\Filo\agents\<SID>`. Installation retains the existing public
access token through earlier-layout cleanup, failures and retries. Invalid public
tokens abort before interruption and remain unchanged; only an absent token is
generated. Other earlier-layout configuration and private credentials are not
migrated. Installation verifies worker and encrypted public readiness. Uninstall
removes those directories and the owned service, tasks, firewall and PATH entry.
See [the installer contract](windows-installer.md) for shutdown and failure behavior.

The public gateway admits `0.0.0.0`, `127.0.0.1` or an explicit tailnet IPv4 bind.
New installs default to all IPv4 interfaces; fixed-layout reinstalls keep their
configured bind unless explicitly overridden. Wildcard readiness uses encrypted
loopback requests. Its managed inbound firewall rule remains limited to tailnet
peers. Private workers, native helpers and the Claude bridge remain authenticated
loopback listeners. Successful installation prints the public access token to
normal PowerShell output after readiness, including SSH, redirected and
non-interactive runs. Failed or non-install operations do not print the token.
Automation must redact the token before retaining installer output.

## Selected sources and identity

Both service and private worker configuration use `sources`, an ordered array of
one to four unique IDs: `codex-desktop`, `codex-cli`, `claude-code` and
`claude-desktop`. An absent field preserves Desktop-only behavior. An explicit
empty, duplicate or unknown source is rejected. `desktopSid` is the selected
ordinary Windows account for all sources;
the existing key name is retained for compatibility. A CLI/Claude worker verifies
its current process SID before any native source connection and refuses SYSTEM or
a different account. Public LocalSystem code never opens those native sources.

Desktop selection creates its task executor directory, IPC client and auxiliary
app-server connection. CLI and Claude Code have independent owned executors;
selecting Desktop, CLI or Claude Code enables the shared raw upload store. Without
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

## Codex CLI

CLI uses only the selected user's native history owner and fresh account/store
scope. Filo-created CLI tasks have their own native writer and support idle Send,
steering, Stop, supported settings, rename, archive and raw attachments. Original
or unknown tasks remain read-only. Source model and usage reads open disposable
native stdio app-servers without any thread or turn operation, verify the native
store/account and fence the result against scope changes. Closing these reads
releases only their own task-free child and never an accepted native task.

Optional `codexDaemonSocket` identifies an existing local socket. Filo does not
start or substitute that shared daemon. Its metadata requires kernel peer identity
and verified actual catalog/auth identity; a matching reported path is insufficient.
Readable history, an executable or a disposable metadata read does not establish
online status. Online status requires a verified live owned executor or existing
daemon connection.

## Claude terminal and Desktop Code Mod observation

`claudeBridgePort` defaults to 7438 and must differ from the public, private worker
and selected native ports. Its listener binds only `127.0.0.1`, with bounded
header/body/write/idle timeouts. Only `/claude-mod/v1/` is handled by the official
Mod bridge. This listener is not mounted on the public service. Both selected
Claude adapters share that listener but retain independent scopes, registrations,
status and observed history. Claude Desktop covers local Code, not Chat or Cowork.

Selecting either Claude source automatically installs the owned observer plugin
under the selected profile's `.claude\skills\filo-observer`. The native plugin
loader discovers it in the next normal session. Terminal Claude Code needs
2.1.287+; Desktop needs its own embedded Code engine 2.1.286+, with Mods enabled
by native policy. Installing files does not establish compatibility or online
status, and the installer never restarts native clients.

The Mod reads `filo-config.json` relative to its native plugin root, without an
environment variable or a manual load step:

```json
{"url":"http://127.0.0.1:7438/claude-mod/v1/","tokenFile":"C:\\FiloWorker\\claude-mod-token","sources":["claude-code","claude-desktop"]}
```

`tokenFile` is absolute and refers to a distinct 256-bit secret inside the existing
private worker directory. An absent file is generated exclusively; an invalid or
reused worker token is refused instead of overwritten. The public service token
never authenticates the bridge. Claude has installation-local isolated scopes,
no claimed Codex account continuity and no execution authority. Only a selected
singleton terminal or desktop attachment can register; a changed, ambiguous or
unselected roster revokes the old binding. Rebinding acquires a fresh nonce and
discards old queued observations. Observation is bounded from registration, with
native IDs/turns preserved. Installed descriptors remain visible offline until
that source has an unexpired native registration. Filo preserves native settings,
authentication, history and transcripts. Component ownership receipts constrain
updates/removal to managed bytes and permit transactional rollback. Stopping the
helper closes only Filo's bridge/client transport and does not cancel native
generation.

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
