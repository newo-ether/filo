package service

import (
	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/protocol"
)

// codexCLIMetadataCaps describes read operations for a configured daemon socket.
// A configured endpoint enables metadata routes while offline, not an online
// claim. Each metadata request still verifies the native peer and store.
//
// The private worker supplies whether its socket is configured. The public proxy
// uses true only as a forwarding ceiling and intersects it with the current
// private descriptor before advertising or dispatching metadata operations.
func codexCLIMetadataCaps(configured bool) protocol.AdapterCapabilities {
	return codexcli.MetadataCapabilities(configured)
}
