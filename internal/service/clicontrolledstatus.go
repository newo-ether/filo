package service

import "context"

// Status reports verified native connectivity. Finding a usable executable only
// permits explicit creation; it does not establish an online native connection.
func (s *CLIControlledSessions) Status(ctx context.Context) AdapterStatus {
	return s.nativeStatus(ctx, resolveCLIRuntime)
}

func (s *CLIControlledSessions) nativeStatus(ctx context.Context, runtime func() (string, error)) AdapterStatus {
	before, err := s.scope(ctx)
	if err != nil {
		return sourceStatus("unavailable", "native_scope_unavailable")
	}
	if _, err := runtime(); err != nil {
		return sourceStatus("unavailable", "native_runtime_unavailable")
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	connected := false
	for _, id := range ids {
		access, err := s.TaskSessions.Access(ctx, id)
		if err == nil && access.Capabilities.Live {
			connected = true
			break
		}
	}
	if !connected && s.sharedFactory != nil {
		page, err := s.liveCatalog(ctx, "")
		connected = err == nil && len(page.Sessions) > 0
	}
	if !connected {
		connected = s.reader.Status(ctx).State == "connected"
	}
	after, err := s.scope(ctx)
	if err != nil || before != after {
		return sourceStatus("unavailable", "native_scope_unavailable")
	}
	if connected {
		return AdapterStatus{State: "connected"}
	}
	return sourceStatus("disconnected", "native_connection_unavailable")
}
