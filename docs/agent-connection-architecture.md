# Agent integration architecture

Filo is an independent service between remote clients and local agent harnesses.
The Windows installer discovers four native sources under one Windows account,
lets the user select them, and installs their required Filo components automatically.
Selection identifies the native source and account, not a running process. WSL
clients are outside this Windows integration.

| Source ID | Role |
| --- | --- |
| `codex-desktop` | Native task control and bounded history through Codex Desktop |
| `codex-cli` | Live-only catalog and control through the existing native daemon; existing Filo-owned execution and task-free metadata reads; no new conversations |
| `claude-code` | Native Mod observation, submitted/observed input, next-request effort and exact-turn Stop; existing Filo-owned execution; no new conversations |
| `claude-desktop` | Native Mod observation, submitted/observed input, next-request effort, exact-turn Stop and bound native Rename; no external model catalog or model switching; no Chat or Cowork support |

A Desktop-only installation needs no separate CLI. CLI-only or Claude-only workers
do not register the Desktop auxiliary Host or allocate Desktop task executors.
CLI execution uses the selected user's native Codex CLI, without a Desktop runtime
fallback. Existing native daemon access requires peer and native store verification;
Filo never starts that daemon implicitly. The public `filo codex` launcher is
removed; hidden legacy owner lifecycle support preserves accepted native work.
Discovering history or an executable grants no control over an existing session.

## Installation and native readiness

An integration is published after its required component installation succeeds.
Installed integrations remain in Agora while offline; only uninstalled integrations
may be hidden. Their descriptors are not filtered by process presence, readable
history or successful observation. Native online state requires independent,
source-specific native readiness evidence. Gateway health, executable discovery
and readable historical files cannot supply that evidence. An offline descriptor
can still advertise supported creation for Codex Desktop; neither CLI source
permits creation. Constructing an adapter does not launch a native session or send
a model request.

Selecting either Claude source installs the persistent helper at
`~/.claude/skills/filo-observer` in the selected user's profile. The normal flow
requires no manual copy, environment variable or loading command. Only Filo-owned
payload and receipts are managed; unrelated or user-modified files are preserved.
Local configuration references the private worker token file without copying the
secret. Claude Code and Claude Desktop keep independent native registrations and
online state even when both use the same helper and loopback bridge. Live observation
starts at registration. Native lifecycle metadata supplies the exact transcript path.
For an existing Desktop registration that attached after that event, the exact persisted
host/engine mapping also permits a lookup of the engine UUID's JSONL under the selected
native projects store. The lookup rejects missing/ambiguous identity, multiple files,
links outside the store and more than 1024 project entries. Both paths expose the same
bounded read-only pages and grant no runtime or control authority.

Claude Mods need terminal Claude Code v2.1.287+ or Desktop's embedded Claude Code
engine v2.1.286+; the Desktop app version or an external CLI version is insufficient.
Installed helper files do not prove a usable Mod connection: observation waits for
compatible, source-matching native registration. Ordinary isolated CLI sessions
qualified persistent skills-directory autoload without a plugin-loading flag. The
installed 2.1.310 integration qualified model/catalog/request-effort observation,
HTTP Stop, non-exact idle Send and running Insert with subsequent model consumption,
and actual model/effort confirmation from later native requests. Desktop's embedded
2.1.308 engine passed plugin validation and enabled-plugin discovery. Its installed
source-matching registration also qualified Send, running Insert with native
consumption, exact Stop and live text/tool/state/title consistency. Model selection
and mobile UI remain separate gates. The native `session.surfaces()`
roster lists current attachments, not launch origin.
Mixed terminal/Desktop attachments are rejected
for source attribution rather than selecting a preferred source. Filo-owned Claude
Code execution uses a separate native stream-json connection and does not require
Mod registration. See the [installer guide](windows-installer.md) for compatibility,
ownership and qualification details.

## Codex Desktop connection priority

1. Discover the original Desktop application, selected user and live IPC owner.
2. Verify actual protocol capabilities before dispatching an operation.
3. Use the unmodified runtime provided by Desktop only for capabilities unavailable
   through the original IPC interface. Never require a separately installed CLI.

Original active tasks remain with their existing owner. An independently started
process cannot control a task merely because it can read the same history or ID.
Discovery is not control authority, and shared files do not grant writer ownership.
Filo-created tasks use their own verified execution scope and independent lifetime.
An explicit Send or settings request may admit an existing ownerless idle thread
in the Desktop source after authoritative native-state and account checks and a
final IPC owner recheck. The native resume lock remains mandatory. Active or unknown
state is refused; list/history/subscription reads never admit a writer.

## Filo-owned CLI and Claude Code execution

New conversations are unsupported for both CLI sources. Their private-worker and
public-gateway descriptors advertise `create:false`, and scoped creation returns
HTTP 501 before native allocation. The source-level Create methods also refuse
without launching an executor or broker. Previously accepted native work keeps its
existing owner, controls and independent lifetime. Desktop creation still requires
the first text message and client identity in the same request; selecting a source
or opening a draft never creates an empty session. Original CLI history outside
admitted native daemon ownership stays read-only. Mod-observed sessions expose only
independently proven operations. Existing Filo-created execution uses its private
owner; normally launched terminals retain the native daemon owner described below.

Filo-owned CLI tasks support idle Send, native active-turn steering, exact-turn
Stop, and model, effort and service-tier settings when supported by the admitted
native executor. Only a proven idle Filo-owned CLI task can resume after executor
retirement; its binding is refreshed and the native writer lock still applies.
Stop cannot allocate an executor for an idle task. Source-level CLI models and
account usage use disposable native stdio connections that never start or resume a
thread and never send a turn. Each read verifies the selected account/store and
fences results against scope changes. The read transport admits only model/list
and account/rateLimits/read after initialization; closing it releases only its
own task-free child. These reads do not establish source online state. Optional
existing shared-daemon metadata retains peer/store verification.
Settings success also requires fresh applied evidence from the same native task.
Earlier stdio-owned qualification on Codex CLI 0.147.0 acknowledged an update
without a settings notification; that result remains unconfirmed and is not
replayed. The explicit shared websocket path separately qualified matching settings
notifications on both native clients. Neither path assumes an independent read-only
current-settings query or confirms an update from an empty acknowledgment.

Filo-owned Claude Code sessions support idle Send, exact-execution Stop, rename,
raw attachments and model/effort selection from the native catalog. Effort changes
require `apply_flag_settings` followed by matching `get_settings` applied evidence.
Active steering, archive and service-tier settings remain unsupported. Model
control success acknowledges native `set_model`; the effective runtime model
remains unknown until the next native `system/init` readback. Stop requires both
the matching interrupt acknowledgment and a native terminal result for that same
admitted execution.

Claude `filo.claude.execution.*` turn values are Filo correlation IDs, not native
Claude turn IDs. Native replayed input UUIDs are retained as
`inputReceipts[].messageId` (`InputReceipt.MessageID`); input acceptance and terminal
completion are separate evidence.

Owned stream-json execution projects bounded live partial assistant text,
thinking, tool-use arguments and matched tool results. Thinking and tools are
activity rows and require an activity-enabled read (`includeActivity=true` on the
conversation route). Partial text/thinking rows require a native message ID, tool
rows require a native tool-use ID, and results update only that same tool within
the admitted execution. Rows lacking the required native identity are not
published. Stream-event UUIDs identify notifications and cannot replace missing
native message IDs. Display IDs may add a block index; the actual native identity
is retained separately. Events with `parent_tool_use_id` are excluded so
child/subagent output cannot be merged into the parent execution. Thinking
signatures and redacted thinking are not projected as public text. The native
window holds at most 128 rows and 8 MiB per session; ordinary text rows retain their
24 KiB serialized cap, while tool rows admit up to 2 MiB including JSON escaping.
Complete tool arguments/results use the common decoded-field preview (256 KiB leaf
text target, 384 KiB serialized field cap). Original Mod tool fields use the same
budgets, a 3 MiB event batch and 4 MiB bridge body; its observation queue is bounded
at 96 events / 8 Mi UTF-16 units. Oversized partial inputs are omitted until the
complete native tool call supplies parseable fields. Oldest rows are evicted to fit.
Oversized text rows are omitted as whole rows; a
discarded prefix cannot reappear as an apparently complete suffix. This is an
observation window, not a complete durable transcript. Source-level Claude Code
model reads use that source's disposable native stream-json initialization without
a user message, session creation, execution broker or upload grant. Mod
`config.list` aliases never override this catalog. Native value IDs, display names
and effort levels are preserved; duplicate value IDs retain their first entry,
while distinct IDs sharing a label stay distinct. Reads close only their own
task-free input and fence returned metadata against account/scope changes. They do not require an
existing Filo session or establish native online state.
Neither Claude source advertises account usage.

Media output rows remain unsupported, and the owned executor denies native
approval requests. Mod-observed original sessions provide bounded native streamed
text, thinking, tool names/arguments/results and running/terminal activity state,
current-model readback, explicitly non-exact submitted/observed input, session-local
next-turn effort selections and exact-turn Stop. Native turn/step/block
coordinates identify text and thinking; native tool-use IDs join tool results.
Append UUID rows are never assigned a turn by text or event order, and assistant
append copies are omitted once native streaming projection is active. Media rows
remain unsupported. Optional bounded native title metadata is read separately
from conversation history and matched to the native session identity. Terminal
metadata includes native `ai-title` / `aiTitle` records; an explicit `custom-title`
rename takes precedence. Title read failures cannot interrupt conversation reads.
The Mod retains one bounded classic title/transcript-path observation for the exact
native session and source, and re-emits it after transport loss creates a fresh
registration. Rows, turns and controls are still discarded. Native lifecycle
start/end, ID or source changes discard this metadata too; a new helper instance
without a fresh classic event has no cached metadata to recover. Unknown titles
stay unknown, and no native hook or write is invoked to obtain them.
Original Mod live rows remain bounded to the current registration. Historical text,
thinking and tool calls come only from the lifecycle-reported, session-qualified
transcript under the selected native projects store. Text/thinking keep native
UUIDs; tool rows use native `tool_use.id` for both display and native identity,
with the assistant envelope UUID as their group and no invented turn. Qualified
user `tool_result.tool_use_id` joins only an earlier exact call; results never
create a second tool row. Missing or ambiguous results leave state unknown,
while matched `is_error` determines failed/succeeded. Activity rows require an
activity-enabled read. Each source page scans at most 8 MiB and emits at most
128 rows / 3 MiB; tool matching may read at most one additional 8 MiB window
newer than that page boundary, within the signed snapshot. Results outside these
windows remain unknown. Tool fields use the shared decoded JSON preview and
2 MiB serialized tool-row bound.
Pages exclude sidechain, metadata and nested-tool records and use registration
time as their upper boundary; history never establishes runtime state, input
acceptance or control authority. Signed cursors bind the store scope, source,
session, registration, page boundary and fixed snapshot end. Appends preserve
older cursors and cannot add results to an old snapshot; a changed boundary or
binding is refused. An initial unavailable or out-of-store transcript leaves the
live window usable, while an old history cursor fails explicitly. Historical
media and branch ancestry are not reconstructed. Native `session.messages()`
supplies neither message UUIDs nor a proven message-to-turn mapping and is not
used to invent identities. `/clear`,
`/resume` and `/branch` revoke the old binding; the continuing timer registers the
new native ID without another `session.start` hook. Rebinding can read qualified
older transcript records but never proves that the session is idle.
The SSE heartbeat refreshes
idle metadata changes even without a native conversation notification. Bounded
content truncation marks the observation window incomplete without clearing an
authoritative active turn; only lost events invalidate the native lifecycle state.
Native authentication and configuration stay
unchanged. Filo-owned CLI tasks support native rename/archive and raw uploads;
Claude Code supports native rename and raw uploads with an explicit private
`--add-dir` grant. Existing brokers and sessions keep their original upload
authority during upgrades; disabling creation does not rewrite those owners.
Unknown mutation outcomes never trigger a replay to the old owner. Claude Desktop
uses only the native Code
Mod path and its independently admitted operations. Unknown
native sessions never gain control from a shared ID or readable history. These
limits preclude full Codex Desktop capability parity.

### Native shared Codex terminals

Normally launched terminals use the selected account's already-running official
shared daemon. The default socket is `app-server-control/app-server-control.sock`
under the verified native home; `codexDaemonSocket` may select another existing
endpoint. Filo never initializes, installs, starts or replaces that daemon.
Embedded/no-daemon terminals stay unavailable for same-owner remote control.
Codex 0.164.0 on an isolated ordinary Windows TUI qualified production Send,
running steering, exact Stop/idle, model/effort applied readback and encrypted
public HTTP. The TUI subscriber displayed the remote response. This qualification
uses the official daemon initialized by native Codex, not a Filo-launched owner,
and does not qualify every runtime, service tier or phone UI.

Connections verify the peer user and native home. Discovery admits at most 30
loaded native IDs and rejects paginated results. Bounded thread reads exclude
subagents, mismatched IDs and unloaded threads. Follow rechecks loaded identity
and scope on a new verified connection. NativeLiveState reads one latest turn
summary plus native notifications; this path never resumes a thread or scans
historical lists. Losing admission removes remote access. Closing an observer
closes only that connection and never the native owner.

Shared Send, steering, settings and exact-turn Stop reuse TaskSessions. Native
client/message/turn receipts, fresh exact-turn state and same-task applied settings
remain required; a transport acknowledgment cannot replace them. The public
`filo codex` launcher and terminal-launch functions are removed. Production
construction neither creates nor falls back to the legacy registry. Hidden owner
and legacy lifecycle support remain so previously accepted work is preserved.
Closing an observer never signals that native owner.

### Claude Code original-terminal control admission

Original terminal Send and running Insert use the explicit `submitted_observed`
contract. The generated native Mod types provide no exact client/message/turn
mapping. An accepted native API call yields only `submitted`. A new same-session
native user item or turn after submission, with fully equal content and the bound
command observation, yields `observed`. These receipts never become exact native
linkage. Timeout yields `unconfirmed`, preserves the draft and blocks any resend;
no network recovery or navigation replays input.

Idle Send uses native `prompt.submit({text, asUser:true})`. With an unknown native
runtime and no exact observed active turn, the same call queues until the native
session is idle; Filo keeps runtime unknown until native lifecycle evidence arrives.
Running Insert uses
native `session.append` with a user-role note. The engine returns that stored row's
UUID and text; only full equality can produce `observed`. This note is context for
the next model request in the loop. It cannot change an already dispatched request
or force another request after the turn ends. An isolated native CLI tool loop
qualified same-turn consumption on the next engine request and changed output.
`session.send` is inter-session messaging and is not used for self-steering.

`turn.abort({turnId})` is implemented independently of Send. A Stop command is bound
to the live registration and exact active turn, delivered once, and succeeds only
with both its matching result and same-turn `turn.complete`. Scope, registration
loss or timeout leaves the outcome unknown and never replays it. The installed
CLI engine qualified exact abort on a self-created turn. The shipped Mod qualified
loopback HTTP Stop with one command result, matching native `turn.complete`, and
same-turn idle readback. Ordinary persistent installed CLI qualification repeated
that gate without `--plugin-dir`. A separate installed Desktop fixture qualified
one exact-turn abort with matching native completion and same-turn idle readback.

Both Mod surfaces observe bounded engine/core model-row choices from
`config.list`, the current `session.model` and observed main-loop `turn.step`
effort. Config choices are unresolved aliases, so they supply neither the public
model directory nor a model mutation grant. Both Mod surfaces advertise only
`effort` in settings and keep it independent of catalog availability. The isolated
CLI bridge qualified these observations before exact-turn Stop. An installed Desktop fixture also qualified catalog/model observation and
actual effort confirmation from a subsequent native request. Request effort is not a
model-specific choice list or a proven session setting; missing data stays
unknown. In one native probe, the launch flag requested `high` while `turn.step`
reported `max`; only the observed request value is published.
`config.set(model)` changed the menu value
without changing `session.model` in the isolated probe. Builtin `model` and
`effort` commands exist, but `command.run(model)` did not return within the bounded
idle probe. Effort changes therefore use only the official `turn.step`
request rewrite, retained in registration-local state for subsequent main turns.
The native start hook's synchronous turn identity admits the first request even
while asynchronous observation is unfinished; settings received during that turn
still wait for the following main turn. No bridge read delays native dispatch.
Model mutations through the Mod are refused before command dispatch.
The current turn and subagents stay unchanged; native configuration is not written.
ACK means pending. Confirmation requires a settled engine/core trace with a native
response and the actual request model/effort. Missing evidence remains pending;
downstream mismatch reports failed with actual values. Named effort choices reflect
request-API values, not a model-specific support guarantee. Filo-created
stream-json sessions retain their separate execution path and guarantees.

### Claude Desktop control admission

Desktop Code creation is unsupported through this integration. Both the private
worker and public gateway advertise `create:false` and reject scoped session
creation with HTTP 501 before native dispatch. The Mod operates on its registered
current session; `session.start` is a lifecycle hook, not a callable constructor.
The retained stream-json execution path serves previously Filo-created Claude
Code sessions only and is never a fallback for Claude Desktop. Neither CLI source
permits new creation. Agora keeps New Chat unavailable without an
unsupported-creation notice in the catalog; existing conversations remain selectable.
This boundary does not assert that Desktop itself lacks an internal create action;
no supported external create path has been established for Filo.

The inspected native Mod interface exposes `prompt.submit` without a native input
message UUID. `session.messages` supplies role/text/tool data without that UUID;
`turn.start` supplies a turn ID, and `turn.complete` supplies a terminal reason.
Exact turn abort support therefore does not prove input acceptance or supply the
public input receipt. Filo must not invent message identity from text, ordering,
notification IDs or a successful submit. Desktop Code uses the same registration-
bound non-exact submitted/observed input, pending request settings and exact Stop
implementation. The terminal stream-json path does not qualify running Insert.
Desktop must qualify its own native user-note append execution path independently.

Qualifying Desktop Send requires a live embedded-engine/Mod handshake, correctly
labeled submitted/observed input with no exact-linkage claim, same-turn terminal and
Stop evidence, unchanged native ownership, and accepted-turn survival when Filo
connections disappear. An installed embedded-engine fixture qualified persistent
source-matching registration, non-exact idle Send, exact-turn Stop with same-turn
completion/idle readback, and effort confirmation from a subsequent native request.
Its accepted turns and native tool calls continued to final output while the SYSTEM
gateway or private worker was stopped; each input had one native user row and all
original native process identities survived. This covers those two Desktop outage
cases, not every source or owned-executor failure scenario. After an owner-performed
Desktop restart and fresh native registration, a separate installed check qualified
running Insert: one native user-role meta note was observed and consumed by the
original read-only tool loop. Twelve native Read activities retained names,
arguments, results and succeeded states. Incremental text, active-user lifecycle
evidence, final cleared idle fields, native title consistency and a separate exact
Stop also passed. These data checks do not establish mobile UI acceptance or a
newly generated/renamed Desktop title update. Installing new helper bytes alone
does not prove an existing native session has reloaded them.

Desktop's Code model menu uses bootstrap `modelSelectorConfig`, with account
`model_selector_config` as a fallback, then selects the `code` surface's entries.
This is separate from the engine/core `config.list` model choice aliases observed
by the Mod. Earlier Filo code copied a selected choice directly into `turn.step.model`;
the rejected Desktop `sonnet` and `opus` requests therefore passed those raw aliases
to the provider rather than going through the engine's internal alias resolver.
The inspected public Mod declarations expose model completion/fork/classification
and current-model readback, but neither the Desktop catalog nor a read-only alias
resolver. Native `config.set(model)` writes user settings and does not satisfy the
registration-local settings contract. No alias mapping is inferred from model
family names or the current model. Desktop model selection is unavailable through
Filo: its descriptor and sessions advertise `models:false`, settings contain only
`effort`, and the public model route returns HTTP 501. Neither another source's
CLI catalog nor a static alias table substitutes for the Desktop catalog.

A separate installed Desktop probe sent `/model deepseek-flash` once through the
existing idle Send path. It returned an explicitly rejected input receipt, with
no new native transcript row, turn or observed model request. The native model
remained unchanged. Read-only fingerprints of global user settings matched before
and after. This path therefore qualifies neither native slash-command execution
nor ordinary-text consumption, session-local switching or a subsequent request on
the requested model. The rejected input was not replayed, and no implementation
was substituted. Manual Desktop `/model` and direct native `command.run` remain
separate, untested paths.

The official Mod session API exposes no archive/delete method. The inspected
Desktop host also includes a built-in `ccd_session` archive tool, which stops its
process and normally cleans the worktree. Its scope and effects do not implement
Filo's preserve-history archive contract. An admitted external path is still
unqualified; see [the native capability evidence](claude-desktop-native-capabilities.md).
Filo leaves the fixture's native records intact; cleanup requires the owner's
native Desktop action.

## Ownership and failure

No desktop focus, keyboard, mouse or page automation is permitted. Filo never
patches Codex, redirects normal startup, rewrites native records, manufactures
approvals or signals the original Desktop/backend. The selected Claude helper is
managed separately from native authentication, history and normal startup settings.
Normal native operation cannot depend on a successful Filo connection.

Stopping the gateway or a Filo helper must not cancel accepted native generation.
An independent observer retains only the identity and lifetime resources needed
for a Filo-owned Codex executor. It may retire that executor after independently
verified completion; it cannot terminate an original task or reuse a stale PID as
authority. CLI execution runners and the Claude execution broker own native work
separately from adapter clients. Client `Close` releases observation and connections
without canceling accepted turns. Stopping Claude's observation bridge leaves its
native sessions independent. Client-close coverage does not by itself qualify
killed gateway/worker isolation. Recovery after broker process death is not
promised.

Mutation failures do not authorize another writer, input replay or a replacement
execution path. Receipts represent native acceptance; actual completion and visible
history reconciliation require their own evidence.

## Protocol and data

The public listener uses the shared Conch authenticated encrypted transport.
Private adapter connections remain authenticated loopback endpoints. Bounded
history pages, separate image reads, bounded uploads and stream backpressure prevent
a large conversation or slow client from requiring an unbounded in-memory snapshot.
Each source advertises its own capability ceiling, narrowed by fresh private-worker
discovery and per-session access. Failed discovery discards earlier authority.
Catalog access can remain a cached read-only projection; control requires fresh
scope and the exact sealed binding, checked again at final dispatch. Private
mutation forwarding preserves that binding and sends one same-source request,
without retries or Desktop fallback. Sharing a listener or canonical session
identity grants no control or attachment capability on another access path.

Native records are read-only. Desktop rename and archive use native APIs; archive
preserves history. Images are authorized by native message identity and revision,
not an arbitrary client-supplied filesystem path.

The integration contracts are [Codex connection](codex-connection-contract.md),
[auxiliary execution](windows-auxiliary.md) and
[encrypted public API](encrypted-public-api.md). Historical feasibility probes and
machine-specific execution evidence are retained privately and are not product
capability or compatibility guarantees.
