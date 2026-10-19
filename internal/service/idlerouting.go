package service

import (
	"context"
	"net"
	"net/http"

	"github.com/newo-ether/filo/internal/sessions"
)

// The public gateway resolves native ownership itself. Only its authenticated
// private helper request can carry exact-task idle recovery admission. The
// helper still revalidates native history and the native writer lock.
func (s *Server) mutationContext(request *http.Request, id, action string) context.Context {
	ctx := request.Context()
	local, ok := ctx.Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if s.layer != "worker" || !ok || !local.IP.IsLoopback() || !s.local(request) || request.Method != http.MethodPost ||
		request.Header.Get("X-Filo-Idle-Recovery") != id {
		return ctx
	}
	switch action {
	case "messages", "settings", "model":
		return sessions.WithIdleRecovery(ctx, id)
	default:
		return ctx
	}
}
