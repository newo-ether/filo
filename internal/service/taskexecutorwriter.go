package service

// executorWriterEnded requires a published writer identity and an ended guardian.
// An unknown or still-starting executor cannot authorize a replacement writer.
func executorWriterEnded(state ExecutorState) bool {
	if state.Phase != "ready" && state.Phase != "disconnected" {
		return false
	}
	if state.NativePid == nil || *state.NativePid <= 0 || processAlive(*state.NativePid) {
		return false
	}
	return state.GuardianPid == nil || !processAlive(*state.GuardianPid)
}
