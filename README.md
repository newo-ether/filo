# Filo

Filo connects [Agora](https://github.com/newo-ether/Agora) to coding agents on your
computer. Its Windows integration lets you browse Codex Desktop tasks, read live
messages, send or steer a turn, stop generation, and manage task history from Agora.

Filo runs independently of Codex. Stopping or removing Filo must leave ordinary
Codex startup, existing tasks and accepted native generation unaffected. It does
not patch Codex, change its normal startup configuration, or automate the desktop.

Experimental macOS support targets Codex Desktop on Apple Silicon. See [macOS support](docs/macos.md) for installation, validation coverage, and limitations.

## Supported clients

The Windows installer discovers four clients and lets you pick any of them under
one Windows account. Their Filo components install automatically.

| Client | What Agora can do |
| --- | --- |
| **Codex Desktop** | Full task workflow: browse and search history, follow live tasks, send, steer, stop, create, rename, archive, change model settings, read images and upload files. Needs no separate Codex CLI. |
| **Codex CLI** | Control terminals you start normally: send, steer, exact-turn stop and model/effort changes on loaded threads. Read history by task ID. No new conversations. |
| **Claude Code** | Mod sessions in the terminal: live text, current model and effort, send/insert, exact-turn stop. Previously Filo-created sessions also support model and effort selection, rename and attachments. No new conversations. |
| **Claude Desktop** | **Code** only (no Chat or Cowork): live text, tools and titles, send, insert while running, exact-turn stop, effort changes and rename. No model selection. |

### Codex Desktop

| Capability | Behavior |
| --- | --- |
| Tasks and history | Search the native catalog; read the latest bounded page, then load older messages on demand. |
| Live tasks | Follow the original owner through verified Desktop IPC; send, steer and stop using native turn identity. |
| New tasks | Create on the first explicit Send, using the unmodified runtime supplied by Desktop when IPC cannot provide creation. Immediate Desktop sidebar visibility is not guaranteed. |
| Task management | Rename and archive through native APIs. Archive preserves the original records. |
| Model settings | Read supported models, thinking levels and service tiers; apply them to subsequent turns. |
| Images and files | Read authorized inline/tool images and upload photos or files to the target computer as unchanged bytes. |
| Usage | Show native usage information when the connected account provides it. |

The adapter checks application identity, account ownership and actual protocol
capabilities. It does not pin a Codex version or take over another writer.
Unavailable operations return explicit errors, and approvals stay in Codex. This
is not full Codex Desktop parity.

### Codex CLI

Start Codex normally. Filo connects to the account's official shared daemon,
which must already be running under the selected Windows account. Filo never
starts or installs it; if the socket is missing, the source stays offline.

Filo verifies the local peer and native handshake, lists at most 30 loaded main
threads and follows only those IDs. It never resumes a thread just to observe it.
Inactive history is read separately by exact task ID. Settings changes count as
applied only after fresh native confirmation for the same task. Qualified on
Codex 0.164.0; other versions and service tiers are not guaranteed.

Older Filo-owned CLI tasks keep bounded history, idle send, steering, stop,
model/effort/tier settings, rename, archive and attachments. The `filo codex`
launcher has been removed; previously accepted owners keep running.

### Claude Code and Claude Desktop

Selecting either Claude client installs an observer Mod at
`~/.claude/skills/filo-observer` in the selected user's profile. No manual copy,
environment variable or plugin flag is needed. It talks to a private bridge on
`127.0.0.1:7438` using its own token file; Filo does not change Claude's
authentication, history or startup.

Requirements: terminal Claude Code **v2.1.287+**, or Desktop's embedded Claude
Code engine **v2.1.286+**. The Desktop app version and a separate CLI install do
not tell you the engine version. A source stays offline until a compatible native
session actually registers. After updating the Mod, restart running sessions so
they load it.

Behavior and limits:

- Send and insert are confirmed as submitted/observed, not exact. An unconfirmed
  input blocks resend and is never replayed.
- Effort changes stay pending until the next native request confirms them.
- Mod sessions cannot change models: the Mod only sees configuration aliases, and
  Desktop's model menu comes from a catalog the Mod cannot read.
- Older history comes from the session transcript (text, thinking and tool pages).
  Media and branch ancestry are not reconstructed, so it is not a complete
  transcript.
- Filo-created Claude Code sessions use a separate stream-json connection with
  their own model catalog. They do not support steering, archive or service tiers,
  and they deny native approval requests.
- Accepted turns keep running if the Filo gateway or worker goes down.
- WSL clients are not supported.

### Configuration and status

Private configuration selects one to four `sources`: `codex-desktop`, `codex-cli`,
`claude-code` and `claude-desktop`. Omitting it means Desktop only. CLI-only or
Claude-only setups do not start the Desktop auxiliary host. The selected account
is still stored as `desktopSid` for compatibility. Optional `codexDaemonSocket`
points at an existing daemon socket; the default is the official one under the
native home.

Every installed client stays listed in Agora while offline. Readiness is checked
per client: a running gateway or readable history does not mean the native client
is online. See the [integration architecture](docs/agent-connection-architecture.md)
and [installer guide](docs/windows-installer.md) for identity, bounds and
qualification details.

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
Run the same command again to upgrade. Reinstalls and earlier-layout migration
preserve the public access token, including interrupted installs and retries.
Invalid existing public tokens abort before interruption and remain unchanged.
The executable is `C:\Program Files\Filo\bin\filo.exe`, added once to machine
PATH; private data and logs live under `C:\ProgramData\Filo`. Earlier layouts are
removed without migrating other configuration or private credentials. A failed
run stops Filo resources and returns an error.
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
selection does not register a Desktop auxiliary Host. Reinstalls keep the existing
public token and verify worker and public readiness before reporting success.
`-ListClients`, `-ValidateOnly` and `-Uninstall` support discovery, preflight and
removal; see the [installer guide](docs/windows-installer.md).

In Agora, open **Remote**, add a device name, and enter a reachable Filo service
URL and its access token. New installations listen on `0.0.0.0` (all IPv4 interfaces);
fixed-layout upgrades preserve their prior bind unless `-Bind 0.0.0.0` is supplied.
Loopback and explicit tailnet IPv4 binding remain available. The managed firewall
rule still permits only tailnet peers; all-interface binding does not add a general
LAN/Internet allow rule. Enter the device's reachable address, never `0.0.0.0`.
After verified successful installation, the installer prints the access token
directly, including SSH, non-interactive and redirected invocations. Keep the token
private and redact it from any retained installer output. Internet relays and browser
clients remain outside the supported scope.

## Encrypted transport

Filo reuses [Conch](https://github.com/newo-ether/conch)'s Go encryption and HTTP
transport code as a regular Go module dependency, pinned in `go.mod` and verified
by `go.sum`. There is no separate Filo cryptographic implementation.

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

The Conch dependency is licensed separately under GPLv3 by the same author.
Earlier Filo snapshots did not contain a project-wide license; this declaration
licenses the current source and does not describe those snapshots as MIT-licensed.

Release ZIPs contain this license and third-party notices. Obtain corresponding
source from the source archive of the same release tag, or check out the exact
revision recorded in the ZIP's `manifest.json`. Build instructions and scripts are
included in that source; Go modules, including Conch, are fetched at the versions
pinned in `go.mod`. Independently installed Codex Desktop remains a separate
product and is not relicensed by Filo.
