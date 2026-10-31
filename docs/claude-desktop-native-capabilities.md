# Claude Desktop native capability boundaries

Filo's Claude Desktop source controls the registered local **Code** session through
Claude's official Mod API. A Desktop renderer method or bundled engine method is
not by itself an external API. Neither Chat nor Cowork is covered.

This document records the native boundary found in a read-only inspection on
2026-10-29. The inspected Desktop app was 3.6556.2.0. That version is evidence,
not a runtime allowlist; future implementations must discover actual capabilities.
No native user session, settings, authentication or application bytes were changed
to obtain this evidence. No model request or UI automation was used.

## Current Filo surface

| Operation | Native boundary | Delivery status |
| --- | --- | --- |
| Observe existing Code sessions | Source-matching Mod registration and native events | Existing integration; registration-local bounded projection |
| Idle Send | `prompt.submit({text, asUser:true})` | Existing submitted/observed receipt; no exact native input linkage |
| Running Insert | `session.append` user meta note | Existing native next-request consumption path |
| Stop | `turn.abort({turnId})` plus matching completion | Existing exact-turn path |
| Effort | Registration-local `turn.step` rewrite and subsequent request evidence | Existing pending/applied contract; no user settings write |
| Historical messages | Source-bound lifecycle transcript path, or exact persisted Desktop host/engine mapping to one unique native transcript; native UUIDs and `tool_use.id` | Bounded older text/thinking/tool pages with exactly qualified tool results plus live registration rows; historical media/branch ancestry remain unsupported |
| Read-only tool discovery | Official Mod `tool.list` with exact registration binding | Bounded unverified names only; no host identity or control grant |
| Explicit self metadata | Fixed `get_session({session_id:"self"})` through `tool.call` | Corrected loaded-host readback qualified after an owner-authorized app restart: exact engine/host mapping, optional title and read-only response; this grants no mutation capability |
| Model directory and switching | Desktop bootstrap catalog; native Mod has no catalog resolver | Unavailable on the current Mod control path |
| New conversation | Built-in `ccd_session.start_session` | Returns registered session ID before asynchronous engine/first-input completion; no exact first-send receipt, so Create remains disabled |
| Rename | Fixed native `set_session_title` between strict self metadata reads | Real temporary rename/restoration passed through native permissions; public production route now uses the same bounded sequence, with fresh per-session binding and persisted identity mapping |
| Archive | Built-in session MCP tool | Native operation exists with process/worktree effects; not Filo's preserve-history-only archive contract |
| Raw attachment Send | Native session directory grant and actual file consumption | Not granted by merely discovering a host send method |
| Account usage | Native host usage tools | No admitted Filo account-usage path |

The existing Send/Insert/Stop/effort qualification and its limits are recorded in
[the architecture contract](agent-connection-architecture.md#claude-desktop-control-admission).
A unit test or successful compilation does not establish installed/native behavior.

## Renderer IPC does not establish external control

The inspected `mainView.js` exposes `LocalSessions` and
`LocalAgentModeSessions` through `ipcRenderer.invoke`. The main process constructs
`createLocalSessionsApi` with a `webContents` identity and binds its implementation
to that renderer. Its internal `setModel`, `setEffort`, creation, update and delete
methods are genuine native operations, but their existence supplies no external
transport or account/session admission for Filo.

Do not inject a renderer, enable debugging, patch the app, alter startup, write its
store or use keyboard/mouse actions to invoke those methods. The bundled
stream-json runtime's `set_model` and `rename_session` methods belong to that
runtime's owner; opening another engine is not control of an existing Desktop task.

Desktop builds its menu from bootstrap `modelSelectorConfig`, with account
`model_selector_config` as a fallback. Mod `config.list` choices are unresolved
engine aliases. A CLI catalog, static family mapping or unknown alias response
cannot replace the Desktop model directory. `config.set(model)` writes native
settings and does not implement registration-local remote model selection.

## Built-in session MCP is a distinct native surface

The installed package registers built-in session MCP servers for Code sessions.
The inspected `ap()` definition exposes session metadata and management tools under
`ccd_session_mgmt`, including `list_sessions`, `get_session`, `list_events`,
`set_session_title`, `archive_session`, `unarchive_session`, `send_message`,
`stop_session`, `set_session_model` and `set_session_effort`. `ccd_session` is a
separate native server name. These are genuine host-backed tools, not new methods
on `$.session`. The host constructs the tools inside its session SDK path;
discovery does not establish a separately callable external MCP listener.

### Exact namespace evidence and remaining precedence gap

A narrow follow-up inspection of the same installed build resolved the alias
rather than inferring the server name from a tool description:

- `.vite/build/index.chunk-Blko8cBb.js` imports alias `t` from
  `./index.chunk-CIsKxfLj.js`.
- That defining module exports `Th` through a getter returning `vMi`, whose
  assignment is `vMi="ccd_session_mgmt"`. The adjacent `fMi="ccd_session"` is a
  different constant. The export getter and assignment begin near character
  offsets 6559708 and 5013150 respectively in the decoded module.
- `ap()` in `index.chunk-Blko8cBb.js`, near character offset 206592, sets
  `serverName:t.Th`, `tools:Xf()` and
  `isEnabled:e=>e.sessionType==="ccd"`. Its `Mu` tool is `get_session`.
  Therefore this definition's full tool name is
  `mcp__ccd_session_mgmt__get_session`, not `mcp__ccd_session__get_session`.
- `InternalMcpServerManager.createProxyServers`, near character offset 445186,
  creates each SDK server with the resolved definition's name and closes its
  handler over the native session context. It passes that context into
  `handleToolCall`; the server is not selected from caller-supplied `source`
  arguments.
- `createAllServers`, near character offset 508559, combines remote/direct
  servers `d`, internal SDK servers `g` and local proxies `h` as
  `{...d,...g,...h}`, then invokes `dropCopiesOfOwnServers`. Before local
  proxies are built, `createStdioProxyServers` applies
  `t.bb(config,"local").kept`. Remote creation also uses `t.xb`; incremental
  creation checks `t.yb` and routes an internal matching name through the
  internal manager. Those filters matter: object-spread order alone does not
  prove that a configured same-name server can overwrite an internal server.

The defining module resolves `bb` to `_ri`, `xb` to `vri` and `yb` to `jX`.
Those functions normalize names and reject reserved prefixes. The reserved set
contains both `ccd_session` and `ccd_session_mgmt`. This proves same-name
protection for the inspected initial local/remote and incremental creation paths.
The SDK retains internal instances in `sdkMcpServers`; `mcp_message` dispatch uses
that map, and `setMcpServers` combines non-SDK entries before SDK entries.

Two static routing gaps remain. `setupMcpAndPlugins` in
`index.chunk-CQPpqBhW.js` merges a later plugin server aggregate from `ot.n`;
reserved-name admission on that path was not established. The native engine's
final merge of SDK, user, project and plugin servers is also absent from the
inspected SDK JavaScript. Static inspection therefore does not establish every
possible Mod tool-name route. The later real exact-bound self readback and
rename/restoration qualify the tested loaded host and fixed sequence. The earlier
fixed `mcp__ccd_session__get_session` refusal fixture proves handling of that
supplied name only; it does not exercise the metadata server. Unverified discovery
still grants no control.

The later real exact-bound sequence supplies host/session routing evidence for
that loaded Desktop registration. The two static merge paths remain incompletely
traced; this does not establish arbitrary tool provenance or a new cryptographic
identity mechanism.

Tools depend on host feature gates, the caller's native session and scope, and
native permission handling. Their presence in application code does not prove
availability to a loaded Mod or permission to control a particular task.
The official Mod API offers `$.mcp.call(server, tool, args)` through the engine's
connected MCP servers. Its declaration explicitly says there is no general
permission prompt for the plugin call. A future adapter cannot assume that using
this API preserves every permission check of a model-initiated tool request.
A future adapter must discover the actual tool and schema, preserve its native
permission checks and correlate the host's identity with the exact Mod session.

The official `$.session` declarations have no rename/title setter or directory
permission grant. `session.authorize` obtains an opaque first-party HTTP credential
handle; it cannot grant a native task access to the Filo upload directory. Private
file existence alone does not establish file authority or consumption.

Important constraints visible in the native definitions:

- `set_session_model` and `set_session_effort` expressly refuse the calling session.
  They target another session and may require the user's native approval. Model
  choices come from `getAvailableCodeModels`, not Mod aliases. The tool result's
  explanatory text is not exact same-task applied-settings evidence.
- `get_session` accepts `self` and returns metadata. It is not a model directory or
  an execution grant. `list_events` is a separate history operation.
- `set_session_title` has native title validation, source precedence and consent
  behavior. A proposed title or a returned tool result alone does not qualify a
  Filo rename: same-task native readback remains necessary.
- `archive_session` stops the process and normally cleans its worktree; it may
  affect side sessions. Native guards refuse active, pinned or protected visible
  sessions and can require native approval. This is materially broader than the
  preserve-record archive behavior Filo exposes for Codex Desktop. It must not be
  substituted silently for that contract.
- `start_session` belongs to the separate `ccd_session` server. It accepts a title
  and task prompt, but returns a textual registered session ID before asynchronous
  engine startup completes. The internal first-message UUID and input disposition
  are not returned. A registered session or temporary visible user row therefore
  cannot prove first-input acceptance; Create remains disabled.
- `delete_session` is destructive removal, distinct from archive. It is not a
  fallback when archive is unavailable.

This corrects a narrower historical observation that only renderer deletion
existed: the inspected current Desktop package contains an actual native session
archive tool too. It does not qualify that tool as Filo's current archive API.

## Mod discovery schema and installed-runtime prerequisite

The official declaration inspected on 2026-10-29 exposes:

```typescript
type ToolInfo = { name: string; description: string; mcp: boolean }
// Methods on EngineInterface.tool:
list: () => Promise<ToolInfo[]>
call: EventCalls['tool']['call']
check: EventCalls['tool']['check']
// Method on EngineInterface.mcp:
call: (server: string, tool: string, args?: Record<string, unknown>) => Promise<McpToolResult>
```

`tool.list` does not return input schemas or trusted SDK-server provenance.
`tool.describe` is a hook event whose input carries the description and provider;
it is not a callable `$.tool.describe` discovery method. `$.mcp` has no declared
server/tool listing method. Do not invent those methods or infer a trusted native
server solely from a `mcp__ccd_session_mgmt__*` name. The external classic-script
`PreToolUseHookInput` declaration includes `mcp_server.source`; that schema is not
the Mod event schema. Native tests show that Mod `classic.PreToolUse` receives the
flat tool-call envelope. A caller can supply `mcp_server` or `source` as ordinary
arguments, so those fields do not authenticate an SDK server. No independent host
provenance channel has been qualified for this Mod path.

The native `plugin test` fixture in `internal/claude/native_guard_host_test.go`
proves that a hook-issued call skips only its calling frame, while sibling and
other-plugin hooks still run. Its native `classic.PreToolUse` guard refuses both
`Read` and the fixed session MCP name, including calls with spoofed SDK arguments.
Those refusal checks do not substitute `tool.call` results. The fixture has no
real configured or SDK-backed MCP server and admits no tool body; it therefore
does not establish positive SDK identification, same-session host readback or a
production guard. Direct native namespace-precedence evidence together with exact
Desktop/session registration could qualify routing; the operation's native
permission behavior and readback still require their own evidence.

`$.tool.call` is a distinct, useful primitive: the declaration says it goes through
the native tool hooks, permission check and dialog, and gets its own tool-use ID.
Use that permission-preserving path for a future qualified rename rather than
assuming direct `$.mcp.call` has the same permission behavior. `tool.check` alone
runs no tool and opens no dialog; its verdict is not an applied rename.

Filo's helper loads `hooks/register.js` with the bounded `hooks/desktop.mjs`
module. Its command handler and private worker delivery path accept
`stop`, `send`, `settings`, `desktop-metadata` and `desktop-rename`; no generic
code-loading or caller-selected MCP command is exposed. The helper calls official
`tool.list` once per Desktop registration without blocking ordinary observation.
It bounds the list, keeps only known session-tool names, discards descriptions and
marks every result unverified. A timed-out physical read cannot multiply across
registrations, and stale results are discarded after session or surface changes.

The private authenticated `POST /claude-mod/v1/discovery` reads cached evidence for
the exact Desktop session and registration nonce. It does not refresh the lease,
consume controls or promote public capabilities. It cannot trigger
`get_session(self)` or `list_events`. These names remain diagnostic evidence;
configured same-name collision and precedence remain unresolved by this inspection.

A separate private authenticated `POST /claude-mod/v1/metadata` explicitly requests
one fixed `get_session({session_id:"self"})` through permission-preserving
`tool.call`. It requires the current Desktop session and registration nonce and
refuses a busy native-control slot. Desktop's host `sessionId` and Mod engine
`cliSessionId` are distinct IDs. The worker resolves their exact association from
one native `Claude-3p/claude-code-sessions/<user>/<account>/*.json` record before
queueing. Both fields and the host-ID filename must agree; a missing, ambiguous
or incomplete bounded scan queues no native command. The scan examines at most
1024 records of at most 256 KiB, with at most 128 user/account entries per level.
The host UUID can differ from the engine UUID: adding or removing `local_` is not
identity resolution. The caller cannot provide an expected host ID.

The fixed command carries only that worker-resolved expected host ID. The Mod
requires exact host-ID equality in native readback while preserving its existing
engine/session, surface and generation guards. Go rechecks the same native
record when accepting a matched/applied result and immediately before emitting
the diagnostic response. Only the engine ID, mapped host ID and optional title
of at most 4096 UTF-8 bytes are returned; they do not update the public title,
history, lease or capabilities. Logical timeout is two seconds
and the HTTP bound is four seconds. A timed-out physical call retains its slot
until actual settlement, including across rebinds. Stale results are discarded.
The original Go HTTP/shipped-JS fixtures, native compiler and four-to-five-file
installer ownership migration passed before this identity repair. One earlier
loaded-host query returned `invalid`; its raw result was not retained, so this
does not prove which parser check failed. After the identity repair, full Go,
Windows, vet and bundle gates passed. An owner-authorized app restart then loaded
the corrected Mod and restored the same Code engine session. One fixed self query
returned `matched`, with exact engine/host mapping, a title and `readOnly:true`.
This qualifies the existing JSON-in-`result.text` envelope for that loaded host;
alternate native envelopes remain unqualified. Failures return only fixed
allowlisted reason codes for result shape, missing/oversized text, JSON parsing,
metadata shape, host mismatch, title shape or unavailable mapping. No raw response,
key names, file path or native error text is returned. The successful read does
not qualify a mutation's permission or applied-title behavior.

A separate private authenticated `POST /claude-mod/v1/rename` takes only the exact
Desktop session ID, registration nonce and requested `title`. It is a mutation,
not the read-only metadata diagnostic. It shares the same one-delivery command
and physical native-call slot. It first calls fixed `get_session(self)` and checks
the strict mapped host ID, then fixed `set_session_title({session_id:"self",title})`,
then a fresh fixed metadata read. Every asynchronous boundary checks the same
registration generation, native session and Desktop-only surface. Only exact
session ID and title equality after the mutation returns `applied`; a native ACK,
proposed title or normalized/case-insensitive match cannot establish success.

Titles must be nonempty, already trimmed and at most 200 UTF-16 code units, matching
the inspected native length check. Native visible-text validation and user-title
replacement consent remain owned by the native tool. All calls use `tool.call`;
Filo supplies no consent override, direct MCP call or arbitrary tool arguments.
Refusal cannot return applied success. The two-second deadline covers the complete
sequence. Timeout, canceled HTTP requests, rebinds and uncertain post-mutation
readbacks remain unknown; no automatic replay is attempted. After logical timeout,
the physical slot persists until actual settlement across rebinds, and the expired
sequence cannot start a later native step. The HTTP bound remains four seconds.

The private diagnostic leaves projected titles/history unchanged. On 2026-10-29,
an owner-authorized real qualification applied one temporary title and restored
the exact original title through this fixed `tool.call` sequence. Both operations
returned exact engine/host identities and exact applied title readbacks. No
permission override, model request or UI automation was used. This qualifies the
tested native permission path; a different user-origin title may still require
native consent, which Filo preserves.

Public `POST /v1/adapters/claude-desktop/sessions/<id>/rename` now shares the same
command queue, waiter and native sequence. Its configured source ceiling permits
Rename; session access requires a current Desktop registration and a unique,
complete persisted engine/host mapping. Every mutation also requires the exact
access binding and unchanged source scope. Cached tool discovery grants neither
permission nor capability. Mapping is checked before delivery, result acceptance
and publication; canceled or changed scope/registration outcomes cannot publish
success. Only an exact applied readback updates the projected catalog/runtime
title and emits a change; no conversation message or native history file is
written. A registration-local readback guard keeps stale disk metadata from
immediately reverting that title, then clears when persisted metadata catches up
or a fresh native title event arrives. Timeout/refusal/unknown outcomes do not
project the requested title or replay the command.

This public Go integration is source-level delivery until its newly built
candidate is installed and verified. The native fixed sequence already passed
real qualification; the public worker/encrypted gateway route is tested with
synthetic native results. Full Desktop parity, other tools, uninspected merge
paths and native permission-dialog UI are not claimed.

The native engine must load this implementation and provide a fresh source-matching
registration before installed discovery is verified. Disk replacement or a Filo
restart does not prove native reload. Native plugin validation and behavior tests
passed without a user session or model request. The later owner-authorized restart
and exact self readback qualify the corrected loaded-host metadata path. The
subsequent real rename/restoration qualifies the bounded mutation sequence.
Other new capabilities remain disabled pending their own gates.

## Acceptance required before exposing more controls

A supported implementation must prove all of the following for the operation it
adds:

1. Reach a callable native API from the registered Desktop Mod or an independently
   admitted external transport, without another writer or UI automation.
2. Verify the same selected account, Desktop source and native session; reject
   mixed surfaces and scope/registration changes before dispatch.
3. Preserve native permissions and user decisions. Do not create an auxiliary
   Desktop session just to evade a self-target restriction.
4. Obtain the native catalog or same-task applied/readback evidence required by the
   public operation. Do not manufacture identity from text, ordering or success ACKs.
5. Deliver each mutation once and leave unknown outcomes unknown. Losing Filo must
   leave the native session and accepted generation independent.
6. For creation, bind the first explicit Send to the actual newly created native
   identity. For attachments, prove the existing task's directory authority and
   unchanged file consumption. For archive, resolve the native worktree/process
   semantics explicitly before exposing a new public promise.

Until those gates pass, source and per-session descriptors must keep unsupported
controls disabled. A terminal stream-json executor cannot stand in for Desktop.

## Evidence locations

Read-only installed-package inspection used `app/resources/app.asar`, specifically
its `mainView.js`, main dispatcher, LocalSessions implementation and bundled
session MCP tool definitions. These paths are diagnostic artifacts of the inspected
build, not production path/version pins.

Public native declarations are maintained in Anthropic's
`anthropics/claude-code` repository at `mods/types/claude-code.d.ts`. The official
Mod reference is `https://code.claude.com/docs/en/plugins/mods/reference`.
The shared private runtime log retains task-specific results and authorizations;
no user transcripts or credentials belong in this document.
