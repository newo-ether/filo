// Package nativescope derives opaque draft/unread identities from native state.
// A matching scope is never permission to attach, read a session or mutate it.
package nativescope

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/newo-ether/filo/internal/history"
)

const maxAuthBytes = 256 << 10

// ErrUnavailable intentionally carries no native path, account or credential.
var ErrUnavailable = errors.New("Native account/store identity is unavailable")

var sidPattern = regexp.MustCompile(`^S-1-[0-9]{1,20}(?:-[0-9]{1,10}){1,15}$`)

// Codex recomputes the scope from the verified selected user's SID, the actual
// compatible catalog file and current auth.json bytes on every call. Desktop and
// CLI must supply the same private worker key and selected Codex home to share it.
// The caller establishes selectedSID; this function grants no access authority.
func Codex(ctx context.Context, key []byte, selectedSID, home string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(key) < 32 || len(key) > 4096 || len(selectedSID) > 184 ||
		!sidPattern.MatchString(selectedSID) || strings.TrimSpace(home) == "" || !filepath.IsAbs(home) {
		return "", ErrUnavailable
	}
	store, err := history.OpenIdentityFile(ctx, home, history.Catalog)
	if err != nil {
		return "", privateError(ctx, err)
	}
	defer store.Close()
	storeID, err := fileIdentity(store)
	if err != nil {
		return "", privateError(ctx, err)
	}
	auth, err := readAuth(ctx, filepath.Join(home, "auth.json"))
	if err != nil {
		return "", privateError(ctx, err)
	}
	// Auth and the selected catalog must still agree with the sampled files.
	// Replacements or concurrent native auth writes fail closed for this call.
	currentStore, err := history.OpenIdentityFile(ctx, home, history.Catalog)
	if err != nil {
		return "", privateError(ctx, err)
	}
	currentID, identityErr := fileIdentity(currentStore)
	closeErr := currentStore.Close()
	if identityErr != nil || closeErr != nil || !bytes.Equal(storeID, currentID) {
		return "", privateError(ctx, ErrUnavailable)
	}
	currentAuth, err := readAuth(ctx, filepath.Join(home, "auth.json"))
	if err != nil {
		return "", privateError(ctx, err)
	}
	if !hmac.Equal(auth, currentAuth) {
		return "", ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	for _, field := range [][]byte{[]byte("filo.native-scope.codex.v1"), []byte(selectedSID), storeID, auth} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = mac.Write(size[:])
		_, _ = mac.Write(field)
	}
	return "nsc1_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func privateError(ctx context.Context, err error) error {
	if cancelled := ctx.Err(); cancelled != nil {
		return cancelled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrUnavailable
}

func readAuth(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Refuse symlinks and non-regular inputs before opening, including FIFOs.
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > maxAuthBytes {
		return nil, ErrUnavailable
	}
	file, err := openAuthFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, maxAuthBytes+1))
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	if len(raw) == 0 || len(raw) > maxAuthBytes {
		return nil, ErrUnavailable
	}
	var auth struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			AccessToken string `json:"access_token"`
			IDToken     string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil ||
		(strings.TrimSpace(auth.APIKey) == "" && strings.TrimSpace(auth.Tokens.AccessToken) == "" &&
			strings.TrimSpace(auth.Tokens.IDToken) == "") {
		return nil, ErrUnavailable
	}
	after, err := file.Stat()
	if err != nil || after.Size() != opened.Size() || int64(len(raw)) != after.Size() ||
		!after.ModTime().Equal(opened.ModTime()) {
		return nil, ErrUnavailable
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
