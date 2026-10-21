package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"

	"github.com/newo-ether/filo/internal/nativescope"
)

// codexSourceScope samples the current verified account/store on demand. Both
// Desktop and CLI use the selected helper's private key, never the public token.
func codexSourceScope(sid string, home func(context.Context) (string, error),
	credential UserWorkerToken) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		path, err := home(ctx)
		if err != nil {
			return "", nativescope.ErrUnavailable
		}
		token, err := credential.Credential(ctx)
		if err != nil {
			return "", nativescope.ErrUnavailable
		}
		key, err := hex.DecodeString(token)
		if err != nil || len(key) != 32 {
			return "", nativescope.ErrUnavailable
		}
		defer clear(key)
		return nativescope.Codex(ctx, key, sid, path)
	}
}

// isolatedSourceScope provides an installation-local namespace for sources
// without verified cross-product account/store continuity. It never coalesces
// Claude with Codex and grants no control capability.
func isolatedSourceScope(token, sid, source string) (string, error) {
	key, err := hex.DecodeString(token)
	if err != nil || len(key) != 32 || !systemServiceSIDPattern.MatchString(sid) {
		return "", errors.New("Private source identity is unavailable")
	}
	defer clear(key)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("filo.isolated-source.v1\x00" + sid + "\x00" + source))
	return "iso1_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
