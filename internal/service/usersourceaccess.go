package service

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/newo-ether/filo/internal/protocol"
)

// Access reads current control authority from the authenticated private worker.
// CatalogAccess deliberately remains a separate, cached read-only projection.
func (source *userSource) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	if !validSessionRef(id) {
		return protocol.SessionAccess{}, errors.New("Invalid private source session")
	}
	if err := protocol.ValidateScope(ctx); err != nil {
		return protocol.SessionAccess{}, err
	}
	scope, err := source.scope(ctx)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	if expected := protocol.NativeScopeID(ctx); expected != "" && expected != scope {
		return protocol.SessionAccess{}, sourceAccessChanged()
	}
	path := source.path("sessions/"+url.PathEscape(id)+"/access") + "?nativeScopeId=" + url.QueryEscape(scope)
	raw, err := source.worker.call(ctx, path, nil)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	var access protocol.SessionAccess
	if err := json.Unmarshal(raw, &access); err != nil {
		return protocol.SessionAccess{}, err
	}
	if access.NativeScopeID != scope || !opaquePattern.MatchString(access.CanonicalSessionRef) ||
		access.Binding == "" || len(access.Binding) > 4096 || strings.ContainsAny(access.Binding, "\x00\r\n") {
		return protocol.SessionAccess{}, errors.New("Unverified private source access")
	}
	access.Capabilities, err = normalizedCaps(access.Capabilities)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	if err := source.checkScope(ctx, scope); err != nil {
		return protocol.SessionAccess{}, err
	}
	if source.attachments {
		caps := access.Capabilities
		access.Capabilities.Uploads = (source.id != claudeCodeSource || caps.Uploads) &&
			(caps.IdleSend || caps.ActiveSend == "native-steer")
	}
	return access, nil
}

func (source *userSource) checkScope(ctx context.Context, expected string) error {
	if !opaquePattern.MatchString(expected) {
		return sourceAccessChanged()
	}
	if err := protocol.ValidateScope(ctx); err != nil {
		return err
	}
	scope, err := source.scope(ctx)
	if err != nil {
		return err
	}
	if scope != expected {
		return sourceAccessChanged()
	}
	return nil
}

func (source *userSource) ValidateAccess(ctx context.Context, id, binding string) error {
	if !opaquePattern.MatchString(protocol.NativeScopeID(ctx)) || binding == "" {
		return sourceAccessChanged()
	}
	access, err := source.Access(ctx, id)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(binding), []byte(access.Binding)) {
		return sourceAccessChanged()
	}
	return nil
}

func sourceAccessChanged() error {
	return CodexRpcError("Private source identity or access binding changed; refresh sources", -32600)
}
