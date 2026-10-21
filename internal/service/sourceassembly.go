package service

import (
	"context"
	"sync"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/sessions"
	"github.com/newo-ether/filo/internal/uploads"
)

// sourceChanges bridges construction order without publishing a partially
// initialized server or retaining a second event hub per source.
type sourceChanges struct {
	mu     sync.RWMutex
	server *Server
}

func (c *sourceChanges) set(server *Server) {
	c.mu.Lock()
	c.server = server
	c.mu.Unlock()
}

func (c *sourceChanges) changed(source, id string) {
	c.mu.RLock()
	server := c.server
	c.mu.RUnlock()
	if server != nil {
		server.AdapterChanged(source, id)
	}
}

func desktopSourceCapabilities(attachments bool) protocol.AdapterCapabilities {
	return protocol.AdapterCapabilities{
		Create: true, History: true, Live: true, IdleSend: true, Stop: true,
		Rename: true, Archive: true, Models: true, Usage: true, LazyMessages: true,
		ActiveSend: "native-steer", HistoryMode: "bounded", Uploads: attachments,
		Settings: []string{"model", "effort", "serviceTier"},
	}
}

func desktopSourceAdapter(surface *sessions.DesktopSessions, scope func(context.Context) (string, error),
	status func() DesktopStatus, usage func(context.Context) (protocol.AccountUsage, error), attachments *uploads.Store) Adapter {
	return Adapter{ID: codexDesktopSource, Family: "codex", Origin: "desktop", Scope: scope,
		Sessions: surface, Usage: usage, Uploads: attachments,
		Capabilities: desktopSourceCapabilities(attachments != nil),
		Status: func() AdapterStatus {
			value := status()
			return AdapterStatus{State: value.State, Error: value.Error}
		},
	}
}

// orderedPrivateAdapters preserves the installation's source order when the
// Desktop transport is assembled after the optional observation transports.
func orderedPrivateAdapters(ids []string, optional []Adapter, desktop Adapter) []Adapter {
	result := make([]Adapter, 0, len(ids))
	for _, id := range ids {
		if id == codexDesktopSource {
			result = append(result, desktop)
			continue
		}
		for _, adapter := range optional {
			if adapter.ID == id {
				result = append(result, adapter)
				break
			}
		}
	}
	return result
}

// CLI and Claude remain private selected-user readers. They share the public
// server's payload/image/hub/upload budgets and never fall back to Desktop.
func gatewaySourceAdapters(ids []string, worker *UserWorker, changed func(string, string), desktop Adapter) []Adapter {
	result := make([]Adapter, 0, len(ids))
	for _, id := range ids {
		if id == codexDesktopSource {
			result = append(result, desktop)
			continue
		}
		sourceID := id
		proxy := newUserSource(worker, id, func(session string) { changed(sourceID, session) })
		family, caps := "codex", sourceHistoryCaps(false)
		if id == claudeCodeSource {
			family, caps = "claude", sourceHistoryCaps(true)
		}
		result = append(result, Adapter{ID: id, Family: family, Origin: "cli", Scope: proxy.scope,
			Sessions: proxy, Status: proxy.status, Capabilities: caps})
	}
	return result
}
