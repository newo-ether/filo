# Windows installer

Filo can select Codex Desktop, Codex CLI and Claude Code under one original Windows
account. Selection is per native source and account, never per running process.
Desktop remains independently usable without installing a separate CLI. CLI-only
and Claude-only installations do not require Desktop and do not register its
auxiliary Host task.

| Selected source | Current capability |
| --- | --- |
| Codex Desktop | Existing native task controls and bounded history through the Desktop adapter |
| Codex CLI | Read-only history in the verified user/store scope; optional metadata from an already-running, verified shared daemon |
| Claude Code | Read-only live observation after the user explicitly loads the Filo Mod; no Send, Stop, steering or pre-registration history |

Detection is not a successful native connection or proof of control support. CLI
and Claude do not acquire a writer or silently fall back to Desktop. The Windows
bundle includes the compiled Go service, NSSM, scripts, licenses and an integrity
manifest. It carries no Node.js runtime, npm package or TypeScript compiler. Claude
runs the small JavaScript Mod in its own native Mod runtime.

## One-command installation

Open Windows PowerShell as administrator and run:

```powershell
Set-ExecutionPolicy Bypass -Scope Process -Force; irm https://raw.githubusercontent.com/newo-ether/filo/main/install.ps1 | iex
```

The entry resolves the latest published Windows release, downloads its bundle and
checksums, verifies SHA-256, then opens the native-source checkbox selector. Choose
one or several sources belonging to the same Windows SID. Mixed-account selection
is rejected. Use complete IDs when the same source is detected in several accounts:
`codex-desktop:<SID>`, `codex-cli:<SID>` and `claude-code:<SID>`. The older
`codex:<SID>` and `codex` aliases still select Desktop only.

Run the same command to upgrade. When `-Clients` is omitted, an existing
installation retains its configured sources and account even when a native client
is temporarily unavailable. A legacy configuration without `sources` remains
Desktop-only. Pass an explicit `-Clients` list to change the selected sources.
Existing public and private tokens, endpoint settings and rollback rules remain.

Latest resolution follows GitHub's official release redirect without an API token.
Once resolved, checksums and ZIP use the same version. HTTPS authenticates the
release source; checksums detect damaged or mismatched files, not an independent
publisher signature. Downloads and extraction have bounded size/deadline checks,
reject unsafe paths and links, and finish verification before running bundled
scripts. Failed downloads leave the installed service running. Temporary downloads
and the caller's TLS settings are restored on success or failure.

Save the root `install.ps1` to pass `-Version`, `-Clients`, `-ListClients`,
`-ValidateOnly`, `-Bind`, `-Port`, `-CodexDaemonSocket`, `-ClaudeBridgePort`,
`-CopyClaudeMod` or `-ClaudeModDirectory`. Explicitly empty client lists are rejected.
Windows ARM64 and other adapters are not qualified by this Windows x64 bundle.

## Offline selection and native endpoints

Extract a complete verified bundle and run `Install-Filo.ps1` from an administrator
PowerShell terminal. `-ListClients` performs read-only discovery. `-ValidateOnly`
checks prerequisites without installing. Without elevation it does not open
protected installed configuration and reports `InstalledStateChecked=false`.
`-Uninstall` retires only verified Filo resources and retains private user data.

The default public address preserves the installation's prior value; for a new
installation it uses the sole tailnet IPv4 address or loopback when ambiguous.
`-Bind` and `-Port` choose an allowed endpoint. Public configuration and token live
under `C:\ProgramData\Filo\service`. Worker state belongs to the selected SID;
legacy `%LOCALAPPDATA%\FiloStandalone` state is preserved when already configured.

Gateway configuration, worker configuration and `launch.json` contain the same
bounded `sources` array. `desktopSid` retains its compatibility name and identifies
the original account for every selected source. Worker and native defaults are
loopback ports 7437 and 7436. Without Desktop, the worker has no `appServerUrl` and
both public and worker info report `sessionMode=multi-source`. Desktop installs
retain the existing public/worker modes.

`-CodexDaemonSocket` accepts an explicit absolute existing local socket path, at
most 107 UTF-8 bytes. It is not an executable path. The selected-user adapter still
verifies the actual socket peer and native store before using it. The installer
never starts a daemon, patches CLI startup or redirects the original app-server.
An existing configured socket is preserved while its daemon is offline; metadata
remains unavailable until native verification succeeds. Readable history and
connected daemon metadata are distinct capabilities.

## Explicit Claude Mod loading

Selecting Claude creates its independent private bridge on `127.0.0.1:7438` by
default. `-ClaudeBridgePort` chooses another unprivileged port and is rejected if it
collides with the configured public, worker or selected Desktop native port. Bridge
startup does not require a running Claude process. Only Claude selection creates
`<worker>\claude-mod-token`; this credential is distinct from the public token.
The private `<worker>\claude-mod.json` contains:

```json
{
  "url": "http://127.0.0.1:7438/claude-mod/v1/",
  "tokenFile": "<absolute-worker-directory>\\claude-mod-token"
}
```

The actual URL uses the configured bridge port. Installer rollback restores this
JSON with the matching worker settings. No native `.claude/settings.json`, global
startup environment, authentication or history is changed.

The bundle includes `internal\claude\mod\.claude-plugin\plugin.json`,
`internal\claude\mod\hooks\hooks.json` and `hooks\register.js`. Copying is a
separate explicit opt-in: fresh interactive installs show an unchecked copy checkbox;
unattended installs pass `-CopyClaudeMod -ClaudeModDirectory <new-directory>`
with Claude selected. The destination must have an existing parent owned by the
selected user, remain inside that user's profile, contain no links, and not be
inside `.claude`. Existing destinations are never overwritten. Normal installation
and upgrade do not copy or load the Mod automatically.

In the selected user's shell, set `FILO_CLAUDE_CONFIG` to the absolute private JSON
path for that shell, then load the copied plugin through Claude Code's explicit
plugin loading entry. For native versions exposing `--plugin-dir`, the invocation
has this form:

```powershell
$env:FILO_CLAUDE_CONFIG = '<absolute-worker-directory>\claude-mod.json'
claude --plugin-dir '<selected-user-owned-mod-directory>'
```

This sets only the current shell's environment. The installer does not invoke
Claude or write a persistent plugin registration. Availability depends on the
native Mod API. The observer reports native `session.append` UUIDs after
registration, not fabricated historical IDs. Loading it does not enable remote
Send or Stop. A healthy Filo gateway alone does not prove Mod loading or observation.

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
resolved. Filo never changes the original client's authentication, history, startup
or global settings. Background launchers create no console windows.

Installation is serialized and snapshots configuration/task registrations. Safe
shutdown fences existing worker mutations; an active or unrecognized native Host
blocks its retirement. Failed candidate startup or readiness restores the prior
Filo state only after candidate shutdown is verified. Accepted native work and
original Desktop/backend processes are never targets of generic cleanup.

Fixture passes establish script behavior, not real installation or native API
compatibility. Real install, update, rollback, uninstall, daemon peer verification,
Claude Mod loading and Agora acceptance remain distinct qualification gates. See
[the connection contract](codex-connection-contract.md). Previously distributed
global-routing packages remain withdrawn.
