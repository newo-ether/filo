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