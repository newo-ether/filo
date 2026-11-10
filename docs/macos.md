# macOS support (experimental)

The primary target is official Codex Desktop on Apple Silicon. The app may be named ChatGPT.app; identity verification does not depend on its name, installation directory, PID, or version. Windows named pipe, SID, and installation implementations remain in place.

## Transport and identity

On macOS, Filo runs as the current non-root user. The default socket is resolved from the system user home as `~/.codex/ipc/ipc.sock`. It must belong to that user and have mode 0600. After connecting, Filo obtains the actual peer through LOCAL_PEERCRED and LOCAL_PEERPID, then verifies its process UID, executable's Apple trust chain, OpenAI Team ID `2DC432GLL2`, and bundle ID `com.openai.codex`. Every reconnect repeats verification.

The bundled CLI is discovered at `Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex` inside the verified app and its signature is checked separately. Changes to this package layout fail explicitly rather than falling back to another executable on PATH. These checks do not defend against a malicious same-user process replacing files after verification or process-exit races.

Currently, only `.codex` under the system user home is supported; custom `CODEX_HOME` is not supported. The configuration retains the `desktopSid` field for format compatibility, using a decimal UID on Darwin. Errors retain FILO_IPC_*, FILO_APP_*, or FILO_RUNTIME_* prefixes and underlying causes without printing credentials.

## Prepare an installation from source

Requirements: local Go (see go.mod), Python 3, and a running official Codex Desktop. Containers, administrator privileges, and a separately installed Codex CLI are not required.

```sh
python3 scripts/macos/prepare.py --directory "$HOME/Library/Application Support/Filo"
```

The script exclusively creates a new directory, builds a native-architecture filo binary, and generates private tokens, configuration, and three launchd plists. It refuses to overwrite an existing installation. It neither modifies Codex files nor registers or starts services automatically. The default ports are 7435 (public gateway), 7436 (private worker), and 7437 (helper app-server), all bound to loopback. Check that these ports are free before preparing the installation. Use `--port` to change the base port.

After reviewing the configuration, register host, worker, and gateway in that order in the current GUI user session:

```sh
root="$HOME/Library/Application Support/Filo"
launchctl bootstrap "gui/$(id -u)" "$root/dev.filo.host.plist"
launchctl bootstrap "gui/$(id -u)" "$root/dev.filo.worker.plist"
launchctl bootstrap "gui/$(id -u)" "$root/dev.filo.gateway.plist"
```

For loading at login, place the three plists in the current user's `~/Library/LaunchAgents`, first checking for existing files or registrations with the same Label. launchd retries nonzero exits with a minimum interval of 15 seconds. Host, worker, and gateway are managed independently: a gateway disconnect does not restart Desktop or the helper host. A worker starting before its host is ready fails and is retried by launchd. Logs are stored under the installation's logs directory and currently require manual rotation.

As in the existing Windows setup, the private app-server binds only to loopback; other programs running as the same user are outside the isolation boundary. Do not stop the helper host while it has active work. Once Filo has no active tasks, stop gateway, worker, and host in that order using `launchctl bootout gui/UID/dev.filo.ROLE`. If plists were copied into LaunchAgents, also handle those files during uninstall. Keep the installation directory, tokens, and task data unless explicitly removing them.

## Connect a phone through Tailscale

In Agora Remote, enter the public gateway URL reachable from the phone and use the installation's `gateway/token` as the access token, not the worker token.

If the Tailscale IPv4 address is assigned to a host interface, add `--bind <Tailscale IPv4>` to the preparation command. Only the public gateway binds to this address. If it changes, update gateway/config.json and reload the gateway when no operation is active.

For userspace networking that does not assign the virtual address to a host interface, keep loopback binding and add a TCP forward on the selected Tailscale instance:

```sh
tailscale serve status --json
tailscale serve --bg --tcp=7435 tcp://127.0.0.1:7435
```

With a multi-profile manager, run equivalent commands against the explicitly selected personal profile. Do not use `serve reset` to overwrite other services. Funnel is not required. The Agora URL is `http://<Mac tailnet address>:7435`; authentication and application data continue to use Filo's encrypted protocol.

## Session catalog

Live status enrichment shares a two-second total budget. Timed-out rows remain unknown rather than being reported as idle; catalog data and pagination cursors are still returned. Exact conversation access and mutations retain their existing verification.

Internal subagent sessions are filtered using `threads.source`, including titled subagents. Untitled ordinary conversations remain visible. Filtering occurs before LIMIT and cursor pagination. Older catalogs without a source column retain their existing read behavior.

## Validation and limitations

```sh
go vet ./...
go test -count=1 -timeout=180s ./...
FILO_MACOS_READ_PROBE=1 go test -v ./internal/desktop -run TestDarwinNativeInitialize -count=1
FILO_MACOS_CATALOG_PROBE=1 go test -v ./internal/service -run '^TestMacOSNativeCatalogRead$' -count=1
FILO_DESKTOP_MANAGEMENT_PROBE=1 go test -v ./internal/service -run '^TestDesktopNativeCatalogManagement$' -count=1
```

The management probe creates an empty test thread in an independent temporary directory and verifies rename, readback, and archive. It starts no model turn and does not use existing conversations.

Apple Silicon validation has covered source builds, identity and protocol initialization, isolated empty-thread management, startup of the three background roles, and phone connectivity, usage, catalogs, ordinary conversation reads, bidirectional messaging, image uploads, and stopping an in-progress turn. Regression tests cover subagent filtering, untitled conversations, pagination order, and status enrichment timeouts.

Still requiring dedicated acceptance: steer, exact-stop isolation across concurrent conversations, creating and running a task from the phone, settings changes, other attachment types, failure isolation during active work, login recovery, and long-running operation. Windows-specific lifecycle and interactive checks have not been run on macOS. Build or unit-test success does not certify these scenarios.
