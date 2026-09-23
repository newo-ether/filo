package service

import (
	"container/list"
	"context"
	"errors"
	"regexp"
	"sync"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
)

const (
	// DefaultPayloadCacheBytes bounds the recoverable payload cache.
	DefaultPayloadCacheBytes = 16 * 1024 * 1024
	maxPayloadRequests       = 3
	maxPayloadIDUnits        = 512
)

// revisionPattern is the identity form a cached payload is keyed by.
var revisionPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// PayloadRequest names one cached message body by identity and revision.
type PayloadRequest struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// PayloadResponse returns the requested message bodies in request order.
type PayloadResponse struct {
	Messages []protocol.Message `json:"messages"`
}

// payloadEntry is one cached body with the byte cost that was charged for it.
type payloadEntry struct {
	key     string
	message protocol.Message
	bytes   int
}

// ConversationPayloads caches bounded message bodies behind the mobile
// topology, which never carries those bodies. The TS original used a Map and
// relied on single-threaded insertion order; the Go form keeps the same
// first-in eviction with an explicit ordered list and a lock.
type ConversationPayloads struct {
	mu       sync.Mutex
	entries  map[string]*list.Element
	order    *list.List
	bytes    int
	maxBytes int
}

// NewConversationPayloads builds a cache bounded by maxBytes. A non-positive
// bound caches nothing, matching the TS default-parameter form when a caller
// passes an explicit zero.
func NewConversationPayloads(maxBytes int) *ConversationPayloads {
	return &ConversationPayloads{
		entries:  map[string]*list.Element{},
		order:    list.New(),
		maxBytes: maxBytes,
	}
}

// RetainedBytes reports how many body bytes the cache currently charges.
func (p *ConversationPayloads) RetainedBytes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bytes
}

func payloadKey(session, id, revision string) string {
	return string(Marshal([3]string{session, id, revision}))
}

// remember stores one body and returns its topology node. A body larger than
// the whole bound is still projected, but never cached.
func (p *ConversationPayloads) remember(session string, message protocol.Message, node *protocol.MessageNode) protocol.MessageNode {
	serialized := Marshal(message)
	projected := protocol.MessageNode{}
	if node != nil {
		projected = *node
	} else {
		projected = MessageNode(message, serialized)
	}
	key := payloadKey(session, message.ID, projected.Revision)
	bytes := len(serialized)
	p.mu.Lock()
	defer p.mu.Unlock()
	if element, ok := p.entries[key]; ok {
		p.order.Remove(element)
		p.bytes -= element.Value.(*payloadEntry).bytes
		delete(p.entries, key)
	}
	if bytes <= p.maxBytes {
		p.entries[key] = p.order.PushBack(&payloadEntry{key: key, message: cloneMessage(message), bytes: bytes})
		p.bytes += bytes
		for p.bytes > p.maxBytes {
			first := p.order.Front()
			if first == nil {
				break
			}
			p.order.Remove(first)
			entry := first.Value.(*payloadEntry)
			delete(p.entries, entry.key)
			p.bytes -= entry.bytes
		}
	}
	return projected
}

// take promotes a cache hit to the newest position, matching the TS delete and
// re-set that keeps recently served bodies alive.
func (p *ConversationPayloads) take(key string) (protocol.Message, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	element, ok := p.entries[key]
	if !ok {
		return protocol.Message{}, false
	}
	entry := element.Value.(*payloadEntry)
	p.order.Remove(element)
	p.entries[key] = p.order.PushBack(entry)
	return entry.message, true
}

// Page reads one bounded body page and caches its bodies. It reuses the node
// projection the pager already computed instead of projecting twice.
func (p *ConversationPayloads) Page(ctx context.Context, source ConversationReader, session, cursor, layer string,
	excludedTurns []string) (protocol.ConversationPage, error) {
	var empty protocol.ConversationPage
	page, err := ReadConversationPage(ctx, source, session, cursor, PageOptions{
		Activity:        true,
		Layer:           layer,
		ExcludedTurns:   excludedTurns,
		IncludeMetadata: true,
	})
	if err != nil {
		return empty, err
	}
	for index, message := range page.Messages {
		var node *protocol.MessageNode
		if index < len(page.Nodes) {
			node = &page.Nodes[index]
		}
		p.remember(session, message, node)
	}
	return page, nil
}

// Topology reads identities and revisions only; no body is returned.
func (p *ConversationPayloads) Topology(ctx context.Context, source ConversationReader, session, cursor,
	layer string) (protocol.TopologyPage, error) {
	var empty protocol.TopologyPage
	page, err := ReadConversationPage(ctx, source, session, cursor, PageOptions{Activity: true, Layer: layer})
	if err != nil {
		return empty, err
	}
	nodes := make([]protocol.MessageNode, 0, len(page.Messages))
	for _, message := range page.Messages {
		nodes = append(nodes, p.remember(session, message, nil))
	}
	return protocol.TopologyPage{
		Nodes:      nodes,
		NextCursor: page.NextCursor,
		Queued:     page.Queued,
		Runtime:    page.Runtime,
	}, nil
}

// Load serves cached bodies, re-reading native history only for a miss. Cache
// eviction is recoverable by a native read; no native writer is acquired.
func (p *ConversationPayloads) Load(ctx context.Context, source ConversationReader, session string,
	requests []PayloadRequest, layer string, exactRevision bool) (PayloadResponse, error) {
	var empty PayloadResponse
	if len(requests) == 0 || len(requests) > maxPayloadRequests {
		return empty, CodexRpcError("Expected 1-3 distinct message identities and revisions", -32602)
	}
	wanted := make(map[string]string, len(requests))
	for _, request := range requests {
		if request.ID == "" || toolpreview.UTF16Len(request.ID) > maxPayloadIDUnits ||
			!revisionPattern.MatchString(request.Revision) {
			return empty, CodexRpcError("Expected 1-3 distinct message identities and revisions", -32602)
		}
		if _, duplicate := wanted[request.ID]; duplicate {
			return empty, CodexRpcError("Expected 1-3 distinct message identities and revisions", -32602)
		}
		wanted[request.ID] = request.Revision
	}
	found := make(map[string]protocol.Message, len(requests))
	for _, request := range requests {
		if message, ok := p.take(payloadKey(session, request.ID, request.Revision)); ok {
			found[request.ID] = message
		}
	}
	cursor := ""
	visited := map[string]bool{}
	for len(found) < len(requests) {
		if visited[cursor] {
			return empty, errors.New("Filo history cursor did not advance")
		}
		visited[cursor] = true
		page, err := ReadConversationPage(ctx, source, session, cursor, PageOptions{Activity: true, Layer: layer})
		if err != nil {
			return empty, err
		}
		for _, message := range page.Messages {
			node := p.remember(session, message, nil)
			revision, request := wanted[message.ID]
			if !request {
				continue
			}
			if _, cached := found[message.ID]; cached {
				continue
			}
			if exactRevision && revision != node.Revision {
				return empty, CodexRpcError("Image changed; reload its message", -32600)
			}
			found[message.ID] = message
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}
	if len(found) != len(requests) {
		return empty, CodexRpcError("Message changed; reload its topology", -32600)
	}
	value := PayloadResponse{Messages: make([]protocol.Message, 0, len(requests))}
	for _, request := range requests {
		value.Messages = append(value.Messages, found[request.ID])
	}
	if Size(value) > MaxResponseBytes {
		return empty, errors.New("Filo payload response exceeds its bound")
	}
	return value, nil
}

// cloneMessage detaches a cached body from the read that produced it, matching
// the TS JSON round trip that stores a fresh object.
func cloneMessage(message protocol.Message) protocol.Message {
	if message.Activity != nil {
		activity := *message.Activity
		message.Activity = &activity
	}
	if message.ImageLinks != nil {
		links := make([]string, len(message.ImageLinks))
		copy(links, message.ImageLinks)
		message.ImageLinks = links
	}
	return message
}
