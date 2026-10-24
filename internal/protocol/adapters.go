package protocol

import "context"

type accessBindingKey struct{}

// WithAccessBinding carries the original native owner binding to final dispatch.
func WithAccessBinding(ctx context.Context, binding string) context.Context {
	return context.WithValue(ctx, accessBindingKey{}, binding)
}

func AccessBinding(ctx context.Context) string {
	value, _ := ctx.Value(accessBindingKey{}).(string)
	return value
}

type scopeValidationKey struct{}
type nativeScopeKey struct{}

// WithScopeValidation carries a request-local identity fence to native dispatch.
func WithScopeValidation(ctx context.Context, validate func(context.Context) error) context.Context {
	return context.WithValue(ctx, scopeValidationKey{}, validate)
}

func ValidateScope(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	validate, _ := ctx.Value(scopeValidationKey{}).(func(context.Context) error)
	if validate != nil {
		return validate(ctx)
	}
	return nil
}

func WithNativeScopeID(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, nativeScopeKey{}, scope)
}

func NativeScopeID(ctx context.Context) string {
	value, _ := ctx.Value(nativeScopeKey{}).(string)
	return value
}

// MultiAdapterVersion is an optional extension to application protocol 2.
const MultiAdapterVersion = 1
const MaxAdapters = 4

// AdapterCapabilities describes only operations proven by a native adapter.
// An absent boolean is false; activeSend and historyMode have explicit values.
type AdapterCapabilities struct {
	Create       bool     `json:"create"`
	History      bool     `json:"history"`
	Live         bool     `json:"live"`
	IdleSend     bool     `json:"idleSend"`
	ActiveSend   string   `json:"activeSend"`
	Stop         bool     `json:"stop"`
	Settings     []string `json:"settings"`
	Rename       bool     `json:"rename"`
	Archive      bool     `json:"archive"`
	Uploads      bool     `json:"uploads"`
	Models       bool     `json:"models"`
	Usage        bool     `json:"usage"`
	LazyMessages bool     `json:"lazyMessages"`
	HistoryMode  string   `json:"historyMode"`
}

// AdapterDescriptor is configured source discovery, not execution authority.
// NativeScopeID and access identities are opaque: no paths or account names.
type AdapterDescriptor struct {
	ID            string              `json:"id"`
	Family        string              `json:"family"`
	Origin        string              `json:"origin"`
	State         string              `json:"state"`
	Error         *string             `json:"error,omitempty"`
	NativeScopeID string              `json:"nativeScopeId"`
	Capabilities  AdapterCapabilities `json:"capabilities"`
}

// SessionAccess separates canonical data identity from a revocable access path.
// Binding belongs to one connection/adapter/native owner generation. A shared
// canonical reference never grants control through a different adapter.
type SessionAccess struct {
	NativeScopeID       string              `json:"nativeScopeId"`
	CanonicalSessionRef string              `json:"canonicalSessionRef"`
	Binding             string              `json:"binding"`
	Capabilities        AdapterCapabilities `json:"capabilities"`
}
