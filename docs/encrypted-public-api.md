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

The public wrapper and raw upload protocol passed isolated Go/Android interoperability.
These tests do not qualify native service assembly, production installation, real
native attachment consumption or Codex failure isolation. Complete those gates before
installing the strict Agora encrypted client. No production downgrade is supported.