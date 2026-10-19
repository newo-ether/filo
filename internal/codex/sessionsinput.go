package codex

// StartTurn starts one turn with the service tier confirmed on this native
// attachment. The native turn API defaults an omitted tier rather than retaining
// the thread setting, so a known value must accompany every new turn.
func (s *NativeTaskSession) StartTurn(threadID string, input []any, clientID string) (any, error) {
	params := map[string]any{"threadId": threadID, "input": input, "clientUserMessageId": clientID}
	s.mu.Lock()
	tier := s.settings[threadID].ServiceTier
	s.mu.Unlock()
	if tier.Known {
		params["serviceTier"] = tier.Value
	}
	return s.rpc.Request("turn/start", params)
}
