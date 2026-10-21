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

## Optional multi-source extension

Protocol 2 discovery may include `extensions: {"multiAdapter": 1}` and up to three
configured `adapters`: `codex-desktop`, `codex-cli`, `claude-code`. Each descriptor
includes family, origin, availability, opaque nativeScopeId and proven capabilities.
Unavailable configured sources remain advertised. A failed scope lookup never reuses
an earlier verified account identity. CLI/Claude-only services use sessionMode
`multi-source`; legacy `/v1/*` routes remain Desktop and never follow source selection.

All source routes use `/v1/adapters/{adapterId}/...`, including catalog, history,
status, events, payloads, images, models, usage, settings, management and uploads.
Session, conversation and topology access metadata separates shared verified native
identity (`nativeScopeId`, `canonicalSessionRef`) from revocable control (`binding`,
capabilities). Shared canonical identity grants no permission on another access path.
Codex canonical identity is base64url(SHA256(JSON(["codex", scope, nativeSessionId]))).
Only verified account/store continuity allows shared drafts or unread records.

Existing-session mutations carry `accessBinding` in the encrypted JSON body. The
server removes it before parsing settings and validates the exact native scope,
owner/connection generation and turn again at dispatch. An arbitrary HTTP header is
not a transport for this field: the unchanged Conch encrypted envelope forwards only
method, path, contentType and body. Pending input, Stop or settings never switch
sources or replay when an owner changes.

Create and upload initialization carry the captured descriptor's `nativeScopeId`
in encrypted JSON. The server independently captures fresh scope, compares it before
admission, removes the field and propagates a local scope validator to final native
dispatch. Private helper forwarding must carry the expected scope in JSON and
reconstruct its own validator; a Go context does not cross HTTP. Subsequent upload
chunks/completion and attachment IDs use authenticated source/scope tokens. History
and catalog cursors are source/scope/session sealed; payload revisions and caches
are source/scope qualified. Payload, upload, image and event budgets remain shared
across the whole device.

Current CLI integration exposes bounded read-only verified history. Claude observes
text rows from open opted-in Mod sessions after registration, within a bounded
window. It does not recover pre-registration history or tool/media/thinking rows.
Neither advertises native mutation authority without proven receipts and terminal
semantics. Fixtures and builds do not qualify actual native runtime equivalence.
