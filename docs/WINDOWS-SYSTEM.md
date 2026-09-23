# Filo Windows SYSTEM gateway

Agora connects to the independent LocalSystem gateway. The gateway discovers the
registered original Codex Desktop and verifies the selected Windows account and
actual task owner before live start, steer, stop or settings operations. Closing
Filo releases its own sockets and followers; it does not terminate the desktop.

Catalog and persisted history are read-only, bounded and use the selected user's
native home. The user helper supplies peripheral native API operations and verified
dormant recovery. It runs as the ordinary user, with an unchanged Desktop runtime;
Codex startup, configuration and credentials never depend on Filo's service lifetime.
Original active tasks never fall through to a different writer after owner loss.

Missing helper credentials fail the affected operation and can recover without
restarting the gateway. Desktop absence must not prevent authenticated Filo info
from being served. Explicit native protocol and ownership failures are preserved;
there is no version allowlist, invented success or automatic replay of writes.

The replacement installer uses bundled dependencies, selected-account tasks and
Filo-private configuration. Before retiring an auxiliary it checks task registration,
process ancestry, account, native idle status and PID identity. Original desktop
processes are excluded. Update failures restore the prior Filo configuration and
registrations. See windows-installer.md for usage and the current qualification gate.

Test native failure isolation and installed behavior independently of unit tests.
See [the connection contract](codex-connection-contract.md) for the verification matrix.
