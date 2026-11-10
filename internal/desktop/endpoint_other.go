//go:build !darwin

package desktop

func DefaultEndpoint() (string, error) { return DefaultPipePath, nil }
