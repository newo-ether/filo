# Raw attachment and usage integration

The public service must receive an uploads.Store rooted in the selected native
user's private Filo data directory, not a temporary directory, a desktop config,
or a working project. The service's selected account must be able to read completed
raw files. Keep credentials in their separate administrator-only storage.

POST /v1/uploads declares original name, MIME, byte size and SHA-256. PUT chunks are
at most 256 KiB and must carry the exact next offset. Completion verifies the entire
file digest before publishing an opaque receipt. Session input accepts receipt IDs,
never caller-controlled target paths. Uploads are target-local original bytes;
the native task receives file references, without extraction or conversion.
Codex Desktop, Filo-owned Codex CLI tasks and attachment-enabled Filo-owned Claude
Code sessions share this raw-byte contract. Claude Code receives the private upload
directory through `--add-dir`, with native default permissions unchanged. An old
broker without that grant never advertises upload access. Upgrades preserve its
accepted work and route new attachment-enabled sessions to a separate broker;
only explicit session absence permits routing to the old owner, never a timeout
or an uncertain mutation result. Mod-observed Claude sessions do not gain uploads.
Actual native file consumption is a separate qualification gate from capability
and upload transport fixtures. Private workers preserve the native attachment
permission even though only the public gateway owns upload storage. A public
source requires both that native grant and its upload store; private upload routes
without a store remain unavailable.
A failed/incomplete upload must not start a native turn. Completed files survive a
service restart. Private incomplete fragments expire or are discarded on restart.

Wire all public requests through Conch's authenticated encryption channel, including
upload chunks, ordinary responses, errors, image bytes and event streams. No
plaintext public fallback is accepted. The shared channel is a separate integration
requirement; adding the raw HTTP handlers alone is not a deployable public service.

ServerOptions.Usage reads NativeMetadata.Usage through the selected native runtime.
This invokes account/rateLimits/read only; it never purchases or redeems credits.
Prefer native named buckets and retain unknown windows as null, not zero.

The isolated source checkpoint passed 364 Go tests and vet. This proves these
subsystems with the current base; it does not prove final public runtime assembly,
real native attachment consumption, service fault isolation, or two-device delivery.
