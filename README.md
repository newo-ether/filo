# Filo

Filo connects [Agora](https://github.com/newo-ether/Agora) to coding agents on your
computer. Its Windows integration lets you browse Codex Desktop tasks, read live
messages, send or steer a turn, stop generation, and manage task history from Agora.

Filo runs independently of Codex. Stopping or removing Filo must leave ordinary
Codex startup, existing tasks and accepted native generation unaffected. It does
not patch Codex, change its normal startup configuration, or automate the desktop.

## Supported integration

The Windows installer discovers **Codex Desktop**, **Codex CLI**, **Claude Code**
and **Claude Desktop**, lets you select clients under one Windows account, and
installs their required Filo components automatically. Codex Desktop provides the
task controls listed below and needs no separate Codex CLI installation.
Filo-owned Codex CLI tasks support bounded native history, idle Send,
native steering, exact-turn Stop and supported model, effort and tier settings.
Normally launched Codex CLI terminals use the account's existing official shared
daemon. Filo connects without starting or installing it; loaded-thread admission
and scope checks do not rely on a wrapper.
Claude Code controls Filo-created native sessions with idle Send, Stop, model and
effort selection, rename and raw attachment input. Its bounded live projection
includes partial assistant text, thinking, tool-use arguments and matched tool
results; activity rows require an activity-enabled read. Filo-owned Codex CLI tasks
also support rename, archive and raw attachments. Other original or unknown CLI
sessions need independently verified native ownership. Claude Mod sessions expose
bounded text, current model, native model options, request effort and exact-turn
Stop. Send/Insert uses explicitly non-exact submitted/observed confirmation; an
unconfirmed input blocks resend and never replays. Session-local model/effort choices
remain pending until a subsequent native engine request proves actual values.
Claude Desktop covers **Code** only, with no Chat or Cowork support. Its embedded
engine qualified registration, non-exact idle Send, running Insert with native
user-note consumption, exact-turn Stop and subsequent native-request effort
confirmation. Its live projection qualified streamed text, native tool arguments,
results and states, active-user lifecycle evidence, and native title consistency.
Accepted generation and native tool calls also continued through gateway and
private-worker outages. The Mod's engine configuration choices are aliases, while
Desktop builds its Code menu from a separate bootstrap model catalog. Passing raw
configuration aliases to the request returned provider errors. The inspected public
Mod API exposes neither that catalog nor a read-only alias resolver; Desktop model
selection remains unqualified.
Claude Code's separate owned executor does not support active steering, archive or service-tier settings,
and its owned executor denies native approval requests. Attachment-enabled native
sessions receive access to the private upload directory under default permissions.
Existing brokers keep their original permissions and sessions during upgrades.
Effort changes require native applied readback. Model selection acknowledges
native control; the effective model remains unknown until the next native
initialization. Claude execution IDs correlate Filo work and are not native turn
IDs; native input acceptance is recorded separately from completion. See the
[integration architecture](docs/agent-connection-architecture.md) for identity,
bounds and per-source limits. This is not full Codex Desktop capability parity.
CLI settings success requires fresh, same-task native applied evidence. Earlier
stdio-owned qualification on Codex CLI 0.147.0 lacked a settings notification;
that result remains unconfirmed and is not replayed. The separately qualified
shared websocket path delivered matching settings updates to both subscribers.
This does not qualify every runtime or settings combination. No independent
read-only current-settings query is assumed.
Mod observation starts at native registration and is not a complete transcript.
WSL clients are outside this Windows integration. Mixed terminal/Desktop surface
attachments are rejected for Claude source attribution.

Private configuration selects one to four `sources`: `codex-desktop`, `codex-cli`,
`claude-code` and `claude-desktop`. Omitting the field preserves Desktop-only
behavior. CLI-only or Claude-only workers do not connect the Desktop auxiliary host
or allocate Desktop task executors. The selected Windows account remains
`desktopSid` for compatibility. Optional `codexDaemonSocket` names an existing local
socket; the default is the existing official control socket under the native home.
Filo does not initialize or start that daemon and never falls back to the legacy
wrapper registry in production. Source model and usage reads use a disposable
native stdio app-server with no thread
or turn operations. The selected account, native store and current scope are
verified before results are returned.
These reads do not mark the source online. Existing daemon metadata still requires
peer and native store identity verification. Native history does not grant
execution authority.

An integration is published after its required components install successfully.
Every installed integration remains listed in Agora while offline; only
uninstalled integrations may be hidden. Native readiness is checked independently
for each source. A running gateway or readable history cannot prove that the native
client is online, and one Claude source's registration cannot make the other online.

Selecting either Claude source installs the persistent observer helper at
`~/.claude/skills/filo-observer` in the selected user's profile. The normal flow
requires no manual copy, environment variable or plugin-loading command. Its local
configuration points to a distinct private token file in the worker directory;
the secret is not copied into the helper. The private bridge defaults to
`127.0.0.1:7438`, separate from the worker on `7437` and the public listener. Filo
does not edit Claude's native authentication, history or normal startup. Stopping
the bridge leaves native sessions independent.

Claude Mods require terminal Claude Code **v2.1.287+**, or Desktop's embedded
Claude Code engine **v2.1.286+**. Desktop's app version and a separately installed
CLI version do not establish its engine compatibility. Installed helper files do
not prove native loading or a usable connection: the source remains listed offline
while waiting for compatible, source-matching native registration. Ordinary isolated
CLI sessions qualified persistent skills-directory autoload without `--plugin-dir`.
The installed 2.1.310 integration qualified model/catalog/request-effort observation,
exact-turn HTTP Stop, non-exact idle Send and running Insert with subsequent model
consumption. Model and effort changes remained pending until real subsequent native
requests confirmed their actual values. Desktop's embedded engine qualified native
plugin validation, persistent source-matching registration, non-exact idle Send,
running Insert with subsequent native consumption, exact-turn Stop, live
text/tool/state/title projection, subsequent-request effort confirmation and
accepted-turn survival through gateway/private-worker outages. Model selection
remains unqualified. The successful Insert check followed an owner-performed
Desktop restart and live registration; updating installed helper files alone does
not establish that an existing native session has reloaded them.
Filo-created Claude Code sessions use
an independent stream-json connection and do not require Mod registration. See the
[installer guide](docs/windows-installer.md) for ownership and qualification limits.

The following capabilities apply to **Codex Desktop**:

| Capability | Behavior |
| --- | --- |
| Tasks and history | Search the native catalog; read the latest bounded page, then load older messages on demand. |
| Live tasks | Follow the original owner through verified Desktop IPC; send, steer and stop using native turn identity. |
| New tasks | Create on the first explicit Send, using the unmodified runtime supplied by Desktop when IPC cannot provide creation. |
| Task management | Rename and archive through native APIs. Archive preserves the original records. |
| Model settings | Read supported models, thinking levels and service tiers; apply supported settings to subsequent turns. |
| Images and files | Read authorized inline/tool images and upload photos or files to the target computer as unchanged bytes. |
| Usage | Expose native usage information when the connected account provides it. |

The adapter checks application identity, account ownership and actual protocol
capabilities. It does not pin a Codex version or silently take over another writer.
Unavailable native operations return explicit errors. Approvals remain in Codex.
The integration depends on native interfaces that can change, so compatibility and
installed-service checks are part of every delivery.

## Native Codex terminal connection

Start Codex normally. Same-owner remote control requires the official shared daemon
to be running already under the selected Windows account and native home. Filo
verifies its local peer and native handshake, lists at most 30 loaded main threads,
and follows only those exact IDs. It never resumes a thread to observe it. Inactive
history is omitted from this catalog; exact-ID history reads remain separate.

Filo does not initialize or install the native daemon package. Missing sockets
leave the integration offline without starting another owner or replaying input.
An isolated ordinary Windows TUI on Codex 0.164.0 qualified exact Send receipts,
running steering, exact-turn Stop with idle readback, native model/effort applied
updates and the encrypted public route. Its terminal subscriber displayed the
remotely executed response. This does not qualify every version or service tier.

The public `filo codex` launcher has been removed. Production construction neither
creates nor falls back to the legacy registry. The hidden legacy owner entry and
its lifecycle support remain for previously accepted native work; removing a
launcher does not terminate those owners. Closing a daemon observer closes only
its connection.

## How it works

```text
Agora
  | authenticated, encrypted application channel
Filo Windows service (LocalSystem)
  | authenticated private loopback connection
Filo adapter (selected Windows user)
  |-- Codex Desktop IPC; unmodified Desktop runtime for missing capabilities
  |-- Codex CLI official shared-daemon RPC and Filo-owned execution
  |-- Codex CLI exact-ID history and task-free model/usage reads
  |-- Claude Code Filo-owned stream-json execution
  `-- Claude Code / Claude Desktop Code native Mod observation and exact-turn Stop
```

Filo's service, adapter, GUI background entry and independent lifetime observer are
implemented in Go. PowerShell handles Windows discovery and transactional
installation. No Node.js, TypeScript compiler or C# runtime helper is required.
Claude runs the observer in its native Mod runtime. Filo-owned Desktop tasks have
an independent lifetime: losing the remote service does not cancel an accepted
turn. Filo never signals the original Desktop backend.

## Install on Windows

Open **PowerShell as administrator** and run this one command, just like Conch:

```powershell
Set-ExecutionPolicy Bypass -Scope Process -Force; irm https://raw.githubusercontent.com/newo-ether/filo/main/install.ps1 | iex
```

The installer downloads and verifies the latest Windows release, discovers local
clients, then shows a checkbox selector. Choose one or more supported native clients
under the same Windows account; their required Filo components install automatically.
Run the same command again to upgrade. Fixed-layout reinstalls preserve the access
token. The executable is `C:\Program Files\Filo\bin\filo.exe`, added once to machine
PATH; private data and logs live under `C:\ProgramData\Filo`. Earlier layouts are
removed without migration. A failed run stops Filo resources and returns an error.
End users need their selected
native clients and the Filo bundle, without Git, Go, Node.js or manual component
downloads. A Desktop-only selection needs no separate CLI. See the
[installer guide](docs/windows-installer.md) for unattended options, offline
installation and integrity checks.

## Build an offline Windows bundle

Building requires Git, the Go version declared in `go.mod`, Windows PowerShell,
and a verified local [NSSM](https://nssm.cc/) executable. End users need the complete
Windows bundle and their selected native clients. They do not need Go; selecting
only Codex Desktop does not require a separate Codex CLI.

From a clean source checkout:

```powershell
git clone https://github.com/newo-ether/filo.git
cd filo
go vet ./...
go test ./...
.\scripts\run-windows-tests.ps1
.\scripts\build-windows.ps1 -WrapperPath 'C:\Tools\nssm.exe' -OutputDirectory '.\dist'
```

The build produces an immutable ZIP with compiled binaries, installation scripts,
third-party notices and a per-file SHA-256 manifest. `C:\Tools\nssm.exe` is an example;
pass the actual path of your verified NSSM installation. Build-time tools and NSSM
are separate prerequisites, not downloaded or executed by the installer.

Extract the complete ZIP. Run `Install-Filo.ps1` from an administrator PowerShell
terminal and select the discovered native clients under one Windows account. The
installer configures their required components automatically. CLI-only or Claude-only
selection does not register a Desktop auxiliary Host. Reinstalls keep the fixed-layout
token and verify worker and public readiness before reporting success.
`-ListClients`, `-ValidateOnly` and `-Uninstall` support discovery, preflight and
removal; see the [installer guide](docs/windows-installer.md).

In Agora, open **Remote**, add a device name, and enter the Filo service URL and its
access token. Use a reachable address accepted by the installer. The current
listener supports loopback and an explicitly selected tailnet IPv4 address; generic
LAN binding, Internet relays and browser clients are outside its supported scope.
Retrieve the service token locally as an administrator and keep it private.

## Encrypted transport

Filo reuses [Conch](https://github.com/newo-ether/conch)'s Go encryption and HTTP
transport code. The exact upstream revision, file hashes and license are retained
in `thirdparty/conch`; there is no separate Filo cryptographic implementation.

Public requests, responses, errors, event streams, images and raw uploads use the
authenticated encrypted channel. The shared implementation binds requests to their
method and target, authenticates response direction and status, and checks stream
sequence and termination. It rejects replay, tampering and plaintext downgrade.
Clients and servers must use matching protocol support; a failed mutation is never
automatically resent after reconnecting.

The access token remains the trust root. Encryption does not protect an already
compromised endpoint or prevent an attacker from blocking traffic. Private adapter
connections stay on authenticated loopback; they are not public compatibility
endpoints. Service secrets are restricted to administrators and SYSTEM, while
per-user adapter credentials remain accessible only to their intended account and
the service. See the [public API contract](docs/encrypted-public-api.md).

## Development

Read the [Codex connection contract](docs/codex-connection-contract.md) and
[architecture guide](docs/agent-connection-architecture.md) before changing code.

| Directory | Responsibility |
| --- | --- |
| `cmd/filo` | Service, adapter, native executor and diagnostic entry points |
| `cmd/filo-background` | Windows GUI entry and independent native lifetime observer |
| `internal/codex`, `internal/desktop` | Native protocol, identity and ownership |
| `internal/history`, `internal/sessions` | Read-only history and task projections |
| `internal/service`, `internal/protocol` | Bounded HTTP/SSE, images, uploads and external records |
| `internal/sourcebudget` | Maintained-source line budget |
| `scripts/windows`, `scripts/tests` | Installation, rollback, discovery and Windows lifecycle fixtures |
| `thirdparty/conch` | Unmodified shared encrypted transport and provenance |

Every maintained source file must stay below the 800-line gate. Changes must pass
Go tests, Windows lifecycle checks and relevant native behavior comparisons before
deployment. Real installation, rollback, failure isolation and mobile acceptance
are distinct checks; a build or unit-test pass does not certify all of them.

[CI](.github/workflows/ci.yml) runs on Windows for pull requests into `main`,
pushes to `main`, and `v*` tags. It uses the Go version in `go.mod`, verifies
modules, runs `go vet ./...` and uncached `go test ./...` (including the source
budget), then runs all default PowerShell fixtures. Interactive Desktop and real
service-installation checks remain separate; CI does not publish or deploy.

Never commit credentials, private logs, raw task transcripts, installed state or
machine-specific diagnostics. The public repository begins at the reviewed Go
baseline; private development evidence and earlier local history are not published.

API failures have stable codes, readable fallback messages and bounded diagnostics.
See [the error contract](docs/errors.md); Agora localizes these codes in its application language.

## License

Copyright (c) 2026 Newo Ether.

The current Filo source is licensed under the **GNU General Public License,
version 3 only (SPDX: GPL-3.0-only)**. See [LICENSE](LICENSE) for the full terms.
This declaration applies to project-owned source, scripts and documentation unless
a file explicitly states another license. Third-party components retain their own
copyright and license terms; see [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt).

Forks and commercial use are welcome. When distributing covered modified versions,
preserve copyright and license notices, identify modifications, and provide the
complete corresponding source under GPLv3. The project license does not imply
upstream endorsement of a modified distribution.

The pinned Conch snapshot in `thirdparty/conch` retains its original MIT license
and recorded upstream provenance. It is not relicensed by this declaration.
Earlier Filo snapshots did not contain a project-wide license; this declaration
licenses the current source and does not describe those snapshots as MIT-licensed.

Release ZIPs contain this license and third-party notices. Obtain corresponding
source from the source archive of the same release tag, or check out the exact
revision recorded in the ZIP's `manifest.json`. Build instructions and scripts are
included in that source. Independently installed Codex Desktop remains a separate
product and is not relicensed by Filo.
