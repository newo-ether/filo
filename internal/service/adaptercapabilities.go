package service

import (
	"context"

	"github.com/newo-ether/filo/internal/protocol"
)

// currentCapabilities only removes permissions from the configured maximum.
// Failed or malformed native discovery never retains the previous advertisement.
func (a *adapterSurface) currentCapabilities(ctx context.Context) (protocol.AdapterCapabilities, error) {
	if a.CapabilitySource == nil {
		return a.Capabilities, nil
	}
	caps, err := a.CapabilitySource(ctx)
	if err == nil {
		caps, err = normalizedCaps(caps)
	}
	if err != nil {
		return protocol.AdapterCapabilities{ActiveSend: "unsupported", Settings: []string{}, HistoryMode: "bounded"}, err
	}
	return intersectCaps(caps, a.Capabilities), nil
}

func (source *userSource) capabilities(ctx context.Context) (protocol.AdapterCapabilities, error) {
	descriptor, err := source.descriptor(ctx)
	caps := descriptor.Capabilities
	if err == nil && source.attachments && (caps.Create || caps.IdleSend) &&
		(source.id != claudeCodeSource || caps.Uploads) {
		caps.Uploads = true
	}
	return caps, err
}
