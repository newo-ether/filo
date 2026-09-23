# Windows installer

Only Codex Desktop is required. Filo does not require a separate CLI, global npm
package, PATH edit or manually selected native executable. Use a complete offline
Filo Windows bundle, which includes the compiled Filo service binary, NSSM, licenses
and its integrity manifest. The bundle carries no Node.js runtime and no TypeScript
compiler; the installer scripts call the compiled binary.

## One-command installation

Open Windows PowerShell as administrator and run:

```powershell
Set-ExecutionPolicy Bypass -Scope Process -Force; irm https://raw.githubusercontent.com/newo-ether/filo/main/install.ps1 | iex
```

The online entry follows Conch's download-and-install workflow: resolve the latest
published release, download its Windows bundle and checksums, verify SHA-256, then
open the existing client checkbox selector. No source checkout, Go, Node.js, separate
Codex CLI or manual ZIP extraction is required. The bundle includes NSSM.
Run the same command to upgrade; existing credentials and rollback semantics remain.

HTTPS authenticates the release source; checksums detect damaged or mismatched assets.
They are not an independent publisher signature. Downloads and ZIP expansion have
size/deadline limits; redirects stay on GitHub HTTPS, archive links/traversal and
unverified files are rejected before any bundled script executes. Temporary downloads
are removed on success/failure. Failed downloads never stop the installed service.

For explicit versions or unattended options, save the root `install.ps1` and run it
with `-Version v0.1.0`, `-Clients <id>`, `-ListClients`, `-ValidateOnly`, `-Bind` or
`-Port`. The root online entry passes those options to the bundled installer.
Windows ARM64 and other agent adapters are not qualified by this Windows x64 package.

## Offline usage

Run Install-Filo.ps1 from an administrator PowerShell terminal. The checkbox list
selects the original desktop account. Unsupported adapters remain disabled.
For unattended installation use -Clients codex:<Windows-SID>; -ListClients shows IDs.
Use -ValidateOnly with a selected client for a read-only prerequisite check.
Use -Uninstall to retire verified Filo services/tasks while retaining private data.
The desktop does not need a CLI cache; Filo prepares unchanged native components
from the registered package into its own per-user runtime directory.
Each preparation verifies the component names and bytes against that current package.
Damaged copies are rebuilt without replacing a running helper or pinning a native build.

The default address preserves an existing installation; otherwise it selects the
single tailnet IPv4 address, or loopback when selection is ambiguous. -Bind and -Port
can specify the permitted endpoint. The access token is stored privately at
C:\\ProgramData\\Filo\\service\\token and is preserved on update.

## Preparing a release

Maintainers run `scripts/prepare-windows-release.ps1 -WrapperPath <verified-nssm.exe>
-OutputDirectory <fresh-directory>` from a clean source checkpoint. It invokes the
existing build, then validates the archive through the same bootstrap parser and
writes `release-assets/filo-windows-amd64.zip`, `install.ps1` and `checksums.txt`.
Publish exactly those three files under one `vMAJOR.MINOR.PATCH` release after the
regression and real installer gates. Publish a complete draft before making it the
latest release; never replace individual assets of an already published release.
The preparation script does not publish, tag or change any installed service.

## Ownership and recovery

The gateway runs as LocalSystem. Auxiliary tasks run under the selected ordinary
Windows account, with limited privileges, and cannot become the desktop's startup
backend. Native versions and executable paths are resolved dynamically; Filo bundle
checksums protect its own artifacts and do not pin a native Codex version.

Installation is serialized. Configuration and task registrations are snapshotted;
failed registration, helper startup, service installation or readiness restores the
previous Filo state. Existing workers fence mutations before migration. An active
auxiliary prevents its retirement; the original desktop/backend is never targeted.
No normal Codex configuration, credentials, history or global startup environment
is rewritten. Background runners create no console windows.

## Qualification

The replacement installer is a candidate until the full real installation, update,
rollback, uninstall and native-failure matrix passes. Mock transaction tests and a
healthy gateway do not prove native usability or phone UI. See the
[connection contract](codex-connection-contract.md) for integration verification.
Previously distributed global-routing packages remain withdrawn.

Without elevation, `-ValidateOnly` verifies the bundle and selected client and reports
`InstalledStateChecked=false`. It never opens protected service configuration. Run
validation as administrator to include current service/account settings. Normal
installation and uninstallation require elevation before reading that private state.
