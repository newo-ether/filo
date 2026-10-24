# Shared encrypted public entry point

Use service.NewPublicServer for the public listener. NewServer remains an internal
application handler for authenticated private loopback executor connections and
application tests. Never expose the plain handler as a compatibility fallback.

thirdparty/conch contains unmodified runtime files from the upstream Conch revision
recorded in UPSTREAM.json, with its license and per-file SHA-256 provenance. Do not
fork the crypto or wire protocol here; apply changes upstream, verify them there,
then refresh these exact files and the manifest. Upstream tests are retained in Conch.

The shared authentication middleware bounds each request body, aggregate active
readers, and the read deadline before authentication. The final HTTP listener must
also bound headers and header time, since a handler cannot limit headers it has not
yet received. Keep streaming write deadlines separate from command execution lifetime.
Both the encrypted public wrapper and private application handler refresh a 15-second
write deadline for each write/flush. A slow reader or flush failure must release the
stream's native attach lease and hub subscription. Public streams are limited to 64
per handler, with admission before native attachment and capacity reclaimed on close.
A long native operation receives no response-write deadline until output begins.

Client success requires authenticated `http_end` followed by HTTP response-body EOF.
Duplicate terminal frames, trailing frames and trailing garbage are rejected by the
shared Go and Android readers. No partial terminal marker authorizes a mutation retry.

`/v1/info` includes the process's `build` identity, independently of protocol version:
component (gateway/helper), source revision, modified state and source provenance. Ready records carry the same identity. Windows builds inject the
verified local source identity; dirty or unknown source never masquerades as a clean
release. Gateway and helper identity are reported by their own endpoints; health
checks do not introduce another native request.

The public wrapper and raw upload protocol passed isolated Go/Android interoperability.
These tests do not qualify native service assembly, production installation, real
native attachment consumption or Codex failure isolation. Complete those gates before
installing the strict Agora encrypted client. No production downgrade is supported.

## Timestamp units

Session `updatedAt` uses integer Unix seconds, including create and rename responses.
Message and topology-node `timestamp` uses integer Unix milliseconds. A native event
without a proven timestamp uses JSON `null`; neither receipt time nor registration
time may substitute for native message time. Clients retain row order and identity
independently of timestamps and display unknown time without inventing a date.
The private Claude broker marks this format with `timestampVersion:1`. An upgraded
adapter converts an unversioned older broker's seconds on read without replacing
that broker or interrupting accepted work. Unknown time remains null across forwarding.

## Optional multi-source extension

Protocol 2 discovery may include `extensions: {"multiAdapter": 1}` and up to four
configured `adapters`: `codex-desktop`, `codex-cli`, `claude-code` and
`claude-desktop`. Each descriptor includes family, origin, availability, opaque
`nativeScopeId` and capabilities. Unavailable configured sources remain advertised.
A failed scope lookup never reuses an earlier verified account identity. Services
without Desktop use sessionMode `multi-source`; legacy `/v1/*` routes remain
Desktop and never follow source selection.

All source routes use `/v1/adapters/{adapterId}/...`, including catalog, history,
status, events, payloads, images, models, usage, settings, management and uploads.
A route's presence does not grant that operation to every source or session:

| Source | Control surface |
| --- | --- |
| `codex-desktop` | Verified native Desktop control and bounded history, subject to current owner capabilities |
| `codex-cli` | Loaded original sessions through the existing verified native daemon; separate creation/control of Filo-owned CLI tasks and bounded exact-ID history |
| `claude-code` | Original Mod observation/catalog/request-effort/exact-turn Stop; separate Filo-owned native stream-json execution |
| `claude-desktop` | Code Mod observation/catalog/request-effort/Stop implementation, pending embedded-engine runtime qualification; no Chat or Cowork support |

Session, conversation and topology access metadata separates shared verified native
identity (`nativeScopeId`, `canonicalSessionRef`) from revocable control (`binding`,
capabilities). Shared canonical identity grants no permission on another access path.
Codex canonical identity is base64url(SHA256(JSON(["codex", scope, nativeSessionId]))).
Only verified account/store continuity allows shared drafts or unread records.

Fresh private-worker discovery can only narrow the configured capability ceiling.
A failed or malformed discovery discards earlier authority. Catalog access may be
cached and read-only; existing-session control requires a fresh private-worker
access read, with scope verified before and after it. Per-session access can narrow
Send, Stop and settings as the owner and active execution change. Clients must use
that access rather than treating source-level capabilities as a session grant.

Existing-session mutations carry `accessBinding` in the encrypted JSON body. The
server removes it before parsing settings and validates the exact native scope,
owner/connection generation and turn again at dispatch. The gateway preserves the
private worker's sealed binding and forwards one same-source mutation with it;
the worker independently validates its own binding before native dispatch. An
arbitrary HTTP header is not a transport for this field: the unchanged Conch
encrypted envelope forwards only method, path, contentType and body. Pending input,
Stop or settings never switch sources or replay when an owner changes.

Create and upload initialization carry the captured descriptor's `nativeScopeId`
in encrypted JSON. The server independently captures fresh scope, compares it before
admission, removes the field and propagates a local scope validator to final native
dispatch. Private creation forwarding carries the captured scope in JSON and
reconstructs its own validator; a Go context does not cross HTTP. Creation also
requires the first message and `clientId`; source selection never creates an empty
session. Subsequent upload chunks/completion and attachment IDs use authenticated
source/scope tokens. History and catalog cursors are source/scope/session sealed;
payload revisions and caches are source/scope qualified. Payload, upload, image and
event budgets remain shared across the whole device. Filo-owned Codex CLI and
Claude Code sessions accept raw attachments when their fresh access permits input.
Claude upload access also requires a native private-directory grant; old brokers
and their sessions retain their original permissions during upgrades.

Filo-owned CLI tasks support idle Send, native active-turn steering, exact-turn
Stop and native-supported model, effort and service-tier settings. Normally
launched CLI sessions use only the already-running verified official shared
daemon and loaded-thread admission; embedded/no-daemon sessions and unknown
history stay read-only. Filo never starts the daemon or substitutes a wrapper.
Resuming a retired Filo-owned CLI task
requires fresh exact-task idle evidence and a new valid binding; Stop cannot
allocate an idle executor. Source-level CLI models and account usage use a
disposable native stdio app-server without thread or turn operations, independent
of any existing CLI daemon. Each read verifies native account/store identity and
current source scope; it cannot acquire a writer or establish online state.
Filo-owned CLI tasks also support native rename and archive; Claude Code supports
native rename but not archive.
CLI settings success requires fresh same-task native applied evidence; an empty
acknowledgment alone returns `native_error` with an unconfirmed outcome and is
never replayed. Real qualification on Codex CLI 0.147.0 observed that limitation:
no settings notification arrived, and the native protocol offered no independent
read-only current-thread settings query. Model and effort update confirmation
remain unqualified on that runtime.

Filo-owned Claude Code sessions support idle Send, Stop, rename, raw attachments,
model selection and effort settings. Effort success requires matching native
`get_settings` applied readback. Active steering, archive and service-tier controls
are unsupported. Model update success is the native `set_model` acknowledgment;
the effective runtime model
remains unknown until the next native `system/init` readback. Stop succeeds only
after the matching interrupt acknowledgment and terminal result for the same
admitted execution. Claude `filo.claude.execution.*` values in turn fields are Filo
correlation IDs, not native turn IDs. The replayed native input UUID is retained in
`inputReceipts[].messageId` (`InputReceipt.MessageID`). A receipt proves input
acceptance, while runtime terminal evidence settles completion.

Claude owned stream-json sessions project bounded live partial assistant text,
thinking, tool-use arguments and matched tool results. Thinking and tool rows use
`activity` and require an activity-enabled read (`includeActivity=true` on the
conversation route). Partial notifications require native message identity; tool
rows require a native tool-use ID, and tool results update only that exact tool in
the admitted execution. A stream-event UUID cannot stand in for a missing native
message ID; rows lacking the required native identity are not published. Display
block IDs may add a block index while retaining the native identity separately.
Child/subagent events with `parent_tool_use_id` are excluded from the parent
projection. Thinking signatures and redacted thinking are not public text. The
native projection retains at most 128 rows, 24 KiB per serialized row and 256 KiB
per session; oldest rows are evicted to stay within the window. Oversized rows are
omitted as whole rows, not exposed as apparently complete truncated suffixes.
These windows are not a complete or durable native transcript. Source-level
Claude Code model reads prefer a verified live Mod catalog; otherwise a disposable
native stream-json initialization reads metadata without a user message, session
creation or execution broker. Each read verifies source scope and closes only its
own task-free input. Neither Claude source advertises account usage.

Media output rows remain unsupported. The owned executor denies native approval
requests. Original or unknown native sessions gain no control from a shared ID or
readable history. Source-matching Mod registration independently admits bounded
text observation, native model options, current model, observed main-loop request
effort and exact-turn Stop. Observation starts at registration and does not recover
pre-registration history or thinking/tool/media rows. Stop is bound to that live
registration and exact turn, delivered once, and requires both its matching command
result and same-turn native completion. Registration loss or an uncertain outcome
never causes replay. Native authentication and configuration remain untouched.

Original Claude Mod inputs use `kind: "submitted_observed"` on Send and bounded
`inputReceipts`. `state: "submitted"` means only native call acceptance. `observed`
requires a new same-registration native user item or turn after submission, full
content equality and a matching command observation. It carries the available
native `messageId` or `turnId`, never an invented exact linkage. A 20-second
observation timeout yields `unconfirmed`; resend is blocked, including a fresh
client ID, and no uncertain command is replayed. Rejected calls remain rejected.
Running Insert appends a native user-role note, observed through the engine's
returned row UUID and full text. It joins subsequent requests in the current loop;
there is no promise to alter an in-flight request or start another request after
completion. Observation confirms the stored input, not model consumption.

Mod model/effort changes are session-local choices for subsequent main turns,
not persistent native configuration writes. `runtime.settingsChange` contains the
command `id`, `requested`, and `state: "pending" | "confirmed" | "failed"`.
ACK stays pending. Confirmation requires the settled engine/core `turn.step`
trace's actual request and a native response; mismatch reports `actualModel`,
`actualEffort` and `turnId`. Missing evidence stays pending. Named effort choices
are allowed request API values, not a per-model capability promise. Current-turn
requests and child agents are unchanged. Actual Desktop Code loading, running
Insert and input/settings/failure-isolation qualification remain separate gates.
Terminal stream-json input queueing does not qualify `native-steer`.
These source-specific limits do not provide full Codex Desktop capability parity.

Independent CLI execution runners and the Claude broker retain accepted turns
when their adapter client closes. Client `Close` does not stop native execution.
This does not qualify killed gateway/worker isolation or promise broker process
death recovery. Fixtures, builds and client-close checks do not by themselves
qualify native runtime equivalence, installation or full failure isolation.
