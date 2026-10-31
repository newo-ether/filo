# Windows installer

Filo can discover Codex Desktop, Codex CLI, Claude Code and Claude Desktop under one original
Windows account and offers each installed native client for selection. Selection is per native
source and account, never per running process. Selecting a source automatically installs and
configures all required Filo components, including the persistent Claude observer helper.
The normal flow never asks users to copy the Mod, set an environment variable or load it manually.
A selected integration is published only after its component installation succeeds. Every installed
integration remains in Agora while offline; only uninstalled integrations may be hidden. Native
readiness is independent for each source. Claude Desktop covers Code, not Chat or Cowork.
WSL clients are outside this Windows integration.
Codex Desktop remains independently usable without installing a separate CLI. CLI-only
and Claude-only installations do not require Desktop and do not register its
auxiliary Host task.

| Selected source | Current capability |
| --- | --- |
| Codex Desktop | Existing native task controls and bounded history through the Desktop adapter |
| Codex CLI | Loaded original sessions through the existing verified native daemon; separate Filo-created task control, rename, archive and raw attachments; task-free native model/usage reads |
| Claude Code | Original Mod session observation, catalog, request effort, exact-turn Stop, non-exact submitted/observed input and next-request model/effort settings; separate Filo-created session control, rename and raw attachments |
| Claude Desktop | Code only; native registration, catalog/model/request-effort observation, non-exact Send and running Insert with native consumption, live text/tool/state/title projection, exact-turn Stop, actual effort confirmation and gateway/private-worker isolation qualified; model selection remains unqualified; no Chat or Cowork support |

Detection is not a successful native connection or proof of control support.
Original CLI control requires loaded-thread admission through its existing native
daemon. Original Claude operations require a source-matching live Mod binding.
Send and running Insert use non-exact submitted/observed receipts; model/effort
choices stay pending until a later real request proves them. Unknown sessions
gain no control from history, and no source silently falls back to Desktop.
The Windows bundle includes the compiled Go service, NSSM, scripts, licenses and
an integrity manifest. It carries
no Node.js runtime, npm package or TypeScript compiler. Claude runs the small
JavaScript Mod in its own native Mod runtime.

## One-command installation

Open Windows PowerShell as administrator and run:

```powershell
Set-ExecutionPolicy Bypass -Scope Process -Force; irm https://raw.githubusercontent.com/newo-ether/filo/main/install.ps1 | iex
```

The entry resolves the latest published Windows release, downloads its bundle and
checksums, verifies SHA-256, then opens the native-source checkbox selector. Choose
one or several sources belonging to the same Windows SID. Mixed-account selection
is rejected. Use complete IDs when the same source is detected in several accounts:
`codex-desktop:<SID>`, `codex-cli:<SID>`, `claude-code:<SID>` and
`claude-desktop:<SID>`. The older `codex:<SID>` and `codex` aliases still select
Codex Desktop only.

Run the same command to upgrade. When `-Clients` is omitted, a fixed-layout
installation retains its configured sources and account even when a native client
is temporarily unavailable. Pass an explicit `-Clients` list to change sources.
The existing public access token is retained across both fixed-layout updates and
earlier-layout migration, including interrupted installs and retries. An invalid
existing public token aborts before services stop and is left unchanged. A token
is generated only when no public token exists. Fixed-layout private tokens and
endpoint settings are retained unless explicit endpoint options override them.
Earlier layouts are stopped and removed without migrating other configuration or
private credentials.
After worker readiness, encrypted public readiness and the fixed entrypoint are
verified, installation prints `Access token: <value>` to normal PowerShell output,
including SSH, non-interactive and redirected invocations. Failed, cancelled,
validation, discovery and uninstall runs never print the token. Automation that
retains installer output must redact the token before writing logs.

Latest resolution follows GitHub's official release redirect without an API token.
Once resolved, checksums and ZIP use the same version. HTTPS authenticates the
release source; checksums detect damaged or mismatched files, not an independent
publisher signature. Downloads and extraction have bounded size/deadline checks,
reject unsafe paths and links, and finish verification before running bundled
scripts. Failed downloads leave the installed service running. Temporary downloads
and the caller's TLS settings are restored on success or failure.

Save the root `install.ps1` to pass `-Version`, `-Clients`, `-ListClients`,
`-ValidateOnly`, `-Bind`, `-Port`, `-CodexDaemonSocket` or `-ClaudeBridgePort`.
Explicitly empty client lists are rejected. The legacy `-CopyClaudeMod` switch is
unnecessary and accepted only with a selected Claude source; `-ClaudeModDirectory`
is no longer supported and is rejected. Windows ARM64 and platforms other than
Windows x64 are not qualified by this bundle.

## Offline selection and native endpoints

Extract a complete verified bundle and run `Install-Filo.ps1` from an administrator
PowerShell terminal. `-ListClients` performs read-only discovery. `-ValidateOnly`
checks prerequisites without installing. Without elevation it does not open
protected installed configuration and reports `InstalledStateChecked=false`.
`-Uninstall` removes the Filo service, tasks, firewall rules, machine PATH entry,
program directory and private data directory. Native authentication and history
remain owned by their original clients.

Executables use one fixed directory: `C:\Program Files\Filo\bin`. The installer
adds it once to machine PATH and broadcasts the environment change, so a newly
opened PowerShell can run `filo`. Scripts and the integrity manifest live under
`C:\Program Files\Filo`; there are no installed hash/version directories.

The default public address preserves a fixed-layout installation's prior value;
new installations bind `0.0.0.0` (all IPv4 interfaces). Pass `-Bind 0.0.0.0` to
change an existing installation in the same run, or select `127.0.0.1` or a tailnet
IPv4 address with `-Bind`. `-Port` selects the public port. Readiness for a wildcard
bind is checked through `127.0.0.1` using the encrypted public protocol. The managed
firewall rule still permits only tailnet peers (`100.64.0.0/10`); wildcard binding
does not add a general LAN/Internet allow rule. Use a reachable device address in
Agora, never `0.0.0.0`. Public configuration, token and logs live under
`C:\ProgramData\Filo\service`. Worker state, logs and
the single unchanged Desktop runtime copy live under
`C:\ProgramData\Filo\agents\<SID>`. Old release/candidate/backup directories and
`%LOCALAPPDATA%\Filo` / `FiloStandalone` are deleted, never migrated.

Gateway configuration, worker configuration and `launch.json` contain the same
bounded `sources` array with one to four entries. `desktopSid` retains its
compatibility name and identifies the original account for every selected source.
Worker and Desktop native defaults are loopback ports 7437 and 7436. Without
Desktop, the worker has no `appServerUrl` and both public and worker info report
`sessionMode=multi-source`. Desktop installs retain the existing public/worker modes.

Installed source descriptors remain published when the native client or worker is
offline. Online state requires source-specific native presence or a current Mod
registration; readable historical files and gateway health cannot establish it.
Installing a client and reading its history do not grant control capabilities.

`-CodexDaemonSocket` accepts an explicit absolute existing local socket path, at
most 107 UTF-8 bytes. It is not an executable path. The selected-user adapter still
verifies the actual socket peer and native store before using it. The installer
never starts a daemon, patches CLI startup or redirects the original app-server.
An existing configured socket is preserved while its daemon is offline. Daemon
metadata remains unavailable until native verification succeeds. Independent
source model/usage reads use disposable task-free native stdio connections and do
not require that socket, create a conversation or mark the source online.

## Automatic Claude observer installation

Selecting either Claude source installs the observer helper at
`~/.claude/skills/filo-observer` under the selected Windows user's profile. The
installer deploys its manifest, hooks and local `filo-config.json` automatically;
users do not copy the Mod, set an environment variable or supply a plugin-loading
command. The local configuration contains the bridge URL, selected Claude sources
and an absolute `tokenFile` path. The secret remains in the private worker directory.

The bridge defaults to `127.0.0.1:7438`, separate from the worker on `7437`.
`-ClaudeBridgePort` chooses another unprivileged port and is rejected if it collides
with the public, worker or selected Desktop native port. Bridge startup does not
require a running Claude process. Only Claude selection creates
`<worker>\claude-mod-token`; this credential is distinct from both the public and
private worker tokens. The private `<worker>\claude-mod.json` records the bridge
URL and token path. Reinstallation writes it with matching worker settings.
No native `.claude/settings.json`, global startup environment, authentication or
history is changed.

The installer manages only its exact payload and ownership receipts. It refuses
to take over an existing non-Filo helper directory or overwrite modified managed
files. Update, deselection and uninstall check ownership and hashes;
unrelated files are preserved. Selecting both Claude sources shares the helper
installation but keeps their native registration and online state independent.
A terminal registration cannot make Claude Desktop online, or vice versa.

The [official Mod requirements](https://code.claude.com/docs/en/plugins/mods/overview#turn-mods-on-or-off)
are terminal Claude Code **v2.1.287+** and Desktop's embedded Claude Code engine
**v2.1.286+**. For Desktop, the engine version in a local Code session's `/status`
matters; neither the Desktop app version nor an external CLI version substitutes
for it. A compatible version is necessary but does not prove native loading.

The helper can be installed while waiting for compatible native registration. Its
source remains listed offline until a current, source-matching Mod registration
succeeds. File installation and fixture passes do not establish a usable connection.
Ordinary isolated CLI sessions qualified persistent skills-directory autoload without
`--plugin-dir`. The installed 2.1.310 integration qualified model/catalog/request-effort
readback, exact-turn HTTP Stop, non-exact idle Send and running Insert with subsequent
native consumption, and actual model/effort confirmation on later native requests.
Desktop's embedded 2.1.308 engine qualified native plugin validation, persistent
source-matching registration, non-exact idle Send, exact-turn Stop and subsequent
native-request effort confirmation. Its original generation and native tool calls
continued through installed SYSTEM gateway and private-worker outages, with unique
accepted input and final native output. A later check after an owner-performed app
restart and fresh registration qualified running Insert with a native user-role
meta note consumed by the same tool loop. Native Read names, arguments, results
and succeeded states, incremental text, active-user lifecycle evidence, native
title consistency and a separate exact Stop passed. Mobile UI remains a separate
acceptance gate. The engine/core configuration aliases differ from Desktop's
bootstrap Code catalog; raw alias request overrides returned provider errors.
The inspected public Mod API provides no catalog or read-only alias resolver,
so model selection remains unqualified. See the
[integration architecture](agent-connection-architecture.md#claude-desktop-control-admission)
for this boundary. Installing changed helper bytes does not prove a running native
session has reloaded them; the installer never restarts a native client.
The native surface roster lists current attachments,
not launch origin. Mixed terminal/Desktop attachments are rejected for source
attribution rather than giving either source priority.
The Mod reports native `session.append` UUIDs after registration. Source-bound
lifecycle transcript metadata additionally permits bounded read-only pages of older
native text, thinking and tool calls. Desktop registrations that missed the lifecycle
event may discover the same file using the exact persisted host/engine mapping and
one unique engine-UUID JSONL under the selected native projects store. This bounded
read-only lookup grants no runtime state or control. Text/thinking keep UUIDs; tools keep native
tool-use IDs and join only qualified matching user results, without inventing turns
or emitting separate result rows. Thinking/tools require activity-enabled reads.
Each source page scans at most 8 MiB / emits at most 128 rows and 3 MiB; matching
may inspect one additional 8 MiB newer window within its signed fixed snapshot.
Out-of-window or ambiguous results leave unknown tool state. An unavailable
transcript leaves initial live reads usable; older cursors fail explicitly and
later appends do not change their tool results. Independently admitted Stop requires
one command result and same-turn native completion. Original idle Send uses native
prompt submission; running Insert appends a native user-role note for subsequent
requests. Both use explicitly non-exact submitted/observed confirmation and block
resend after an unknown outcome. Model/effort choices are local to the registration
and confirm only from a later native engine request. Creation and uploads remain
unsupported on this Mod path. A healthy gateway or readable old observations cannot
establish native online state. Claude Desktop integration covers Code only, with no
Chat or Cowork support.

For observer changes, validate the shipped module with each selected native engine
before packaging. Set `FILO_CLAUDE_MOD_VALIDATOR` to that installed engine's trusted
executable and run `go test ./internal/claude -run TestOfficialMod -count=1`.
This invokes the real `plugin validate --json` compiler and native-hosted tests in
addition to JS fixtures; fixtures alone cannot check native loading or middleware.
The plain-JavaScript observer fixture is copied to a disposable native-kit test
input because the kit discovers only `.test.ts` filenames. No TypeScript source,
compiler or runtime dependency is maintained or shipped. The observer subscribes
only to native prompt/response doors, avoiding protected context and internal
notices. The tests include an upstream policy skipping protected context while
native rows are stored once; validation requires a safe pass-through fallback.
These checks start no session or model turn. They are maintainer gates, not an
end-user CLI prerequisite, and do not prove live autoload.

## Preparing a release

Maintainers run `scripts/prepare-windows-release.ps1 -WrapperPath <verified-nssm.exe>
-OutputDirectory <fresh-directory>` from a clean source checkpoint. It builds and
validates the archive and writes `release-assets/filo-windows-amd64.zip`,
`install.ps1` and `checksums.txt`. Publish exactly those three files as a complete
versioned release after the regression and real installer gates. Never replace
individual assets of an already published release. Preparation does not publish,
tag or change an installed service.

For an authorized local build from pending edits, `-AllowModifiedSource` on
`scripts/build-windows.ps1` produces a content-digest candidate. Its manifest records
HEAD, modified state and source SHA-256; source changes during compilation invalidate
the bundle. Published release preparation still requires clean source.

## Ownership and qualification

The gateway runs as LocalSystem. Its selected-account worker and optional Desktop
Host use limited ordinary-user tasks. Native versions and paths remain dynamically
resolved. Filo preserves native authentication, history, normal startup and global
settings; the selected Filo observer helper has its own managed directory and
receipts. Background launchers create no console windows.

Installation is serialized. Before replacement it retires exact Filo service/task
registrations, detached launchers and supervisors, then processes whose executable
paths are inside verified installation directories. PowerShell ownership requires
the actual `-File` argument to name the installed supervisor; arbitrary command-line
mentions and repository paths do not establish ownership. Native executables outside
those directories are not terminated through a process-tree kill.

Each run replaces the fixed program directory, prepares private state and PATH,
registers and starts selected-account tasks and the LocalSystem service, then
checks authenticated worker readiness and encrypted public readiness. Task
registration uses a hidden native Task Scheduler helper with a 20-second deadline.
A failed run stops Filo resources and exits with a precise error; it does not claim
to restore an earlier binary. Repeating a successful run keeps one installed
version and the existing public access token, including earlier-layout migration.
Accepted native work and original
Desktop/backend processes remain outside generic cleanup.

Fixture passes establish script behavior, not real installation or native API
compatibility. Real install, repeated install, uninstall, daemon peer verification,
native Mod autoload and registration on each compatible Claude surface, and Agora
acceptance remain distinct qualification gates. See
[the connection contract](codex-connection-contract.md). Previously distributed
global-routing packages remain withdrawn.
