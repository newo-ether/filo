# Filo

Filo connects [Agora](https://github.com/newo-ether/Agora) to coding agents on your
computer. Its Windows integration lets you browse Codex Desktop tasks, read live
messages, send or steer a turn, stop generation, and manage task history from Agora.

Filo runs independently of Codex. Stopping or removing Filo must leave ordinary
Codex startup, existing tasks and accepted native generation unaffected. It does
not patch Codex, change its normal startup configuration, or automate the desktop.

## Supported integration

The implemented adapter is **Codex Desktop on Windows x64**. Install and sign in to
the original Desktop application; a separate Codex CLI installation is unnecessary.
Claude Code, OpenCode and other adapters are not implemented yet.

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

## How it works

```text
Agora
  | authenticated, encrypted application channel
Filo Windows service (LocalSystem)
  | authenticated private loopback connection
Filo adapter (selected Windows user)
  |-- original Codex Desktop IPC for existing tasks
  `-- unmodified Desktop runtime for missing peripheral capabilities
```

Filo's service, adapter, GUI background entry and independent lifetime observer are
implemented in Go. PowerShell handles Windows discovery and transactional
installation. No Node.js, TypeScript compiler or C# runtime helper is required.
Filo-owned native tasks have an independent lifetime: losing the remote service
does not cancel an accepted turn. Filo never signals the original Desktop backend.

## Install on Windows

Open **PowerShell as administrator** and run this one command, just like Conch:

```powershell
Set-ExecutionPolicy Bypass -Scope Process -Force; irm https://raw.githubusercontent.com/newo-ether/filo/main/install.ps1 | iex
```

The installer downloads and verifies the latest Windows release, discovers local
clients, then shows a checkbox selector. Choose the account containing Codex Desktop.
Run the same command again to upgrade. Your access token is preserved; failed
installation restores the prior Filo registration. You do not need Git, Go, Node.js,
a separate Codex CLI or manual downloads. See the [installer guide](docs/windows-installer.md)
for unattended options, offline installation and integrity/rollback details.

## Build an offline Windows bundle

Building requires Git, the Go version declared in `go.mod`, Windows PowerShell,
and a verified local [NSSM](https://nssm.cc/) executable. End users need the complete
Windows bundle and Codex Desktop; they do not need Go or a separate Codex CLI.

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
terminal and select the Windows account containing the original Codex Desktop.
The installer discovers supported clients and offers a checkbox selection. It
preserves the token on updates and restores the previous Filo registration if an
update fails. `-ListClients`, `-ValidateOnly` and `-Uninstall` support discovery,
preflight and removal; see the [installer guide](docs/windows-installer.md).

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
