package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/protocol"
)

// userSourceAssembly owns only Filo's optional observation transports. Desktop's
// auxiliary metadata client and task executors remain with standaloneGateway.
type userSourceAssembly struct {
	adapters      []Adapter
	cli           *codexcli.Adapter
	bridge        http.Handler
	bridgeAddress string
}

func (a *userSourceAssembly) close() {
	if a != nil && a.cli != nil {
		a.cli.Close()
	}
}

func assembleUserSources(config map[string]any, directory, token string, changed func(string, string)) (*userSourceAssembly, error) {
	ids, err := configuredSources(config)
	if err != nil {
		return nil, err
	}
	assembly := &userSourceAssembly{}
	if !hasConfiguredSource(ids, codexCLISource) && !hasConfiguredSource(ids, claudeCodeSource) {
		return assembly, nil
	}
	sid, _ := config["desktopSid"].(string)
	if !systemServiceSIDPattern.MatchString(sid) {
		return nil, errors.New(systemServiceSIDRefusal)
	}
	if err := verifySourceUser(sid); err != nil {
		return nil, err
	}
	for _, id := range ids {
		switch id {
		case codexCLISource:
			socket, err := configuredDaemonSocket(config)
			if err != nil {
				assembly.close()
				return nil, err
			}
			home := nativeHome(sid)
			scope := codexSourceScope(sid, home, StaticUserWorkerToken(token))
			cli, err := codexcli.New(codexcli.Options{
				History: history.NewNativeHistory(home), Scope: scope,
				SocketPath: socket, UserID: sid, VerifyDaemon: verifySourceDaemon(sid, home, StaticUserWorkerToken(token)),
				SessionIdentity: func(ctx context.Context, id string) (protocol.SessionAccess, error) {
					value := cachedSourceReadScope(ctx)
					if value == "" {
						var err error
						value, err = scope(ctx)
						if err != nil {
							return protocol.SessionAccess{}, err
						}
					}
					return codexReadIdentity(value, id), nil
				},
			})
			if err != nil {
				assembly.close()
				return nil, err
			}
			assembly.cli = cli
			assembly.adapters = append(assembly.adapters, Adapter{ID: id, Family: "codex", Origin: "cli",
				Scope: scope, Sessions: &sourceReader{SessionAPI: cli, scope: scope}, Capabilities: sourceHistoryCaps(false),
				Status: func() AdapterStatus {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					status := cli.Status(ctx)
					return sourceStatus(status.State, status.Reason)
				},
			})
		case claudeCodeSource:
			address, err := configuredClaudeBridge(config)
			if err != nil {
				assembly.close()
				return nil, err
			}
			secret, err := claudeModToken(filepath.Join(directory, "claude-mod-token"), token)
			if err != nil {
				assembly.close()
				return nil, err
			}
			scope, err := isolatedSourceScope(token, sid, id)
			if err != nil {
				assembly.close()
				return nil, err
			}
			bridge, err := claude.New(claude.Config{Token: secret, NativeScopeID: scope,
				OnChange: func(session string) {
					if changed != nil {
						changed(claudeCodeSource, session)
					}
				},
			})
			if err != nil {
				assembly.close()
				return nil, err
			}
			assembly.bridge, assembly.bridgeAddress = bridge, address
			assembly.adapters = append(assembly.adapters, Adapter{ID: id, Family: "claude", Origin: "cli",
				NativeScopeID: scope, Sessions: bridge, Capabilities: claude.Capabilities(),
				Status: func() AdapterStatus {
					value := bridge.Status()
					return AdapterStatus{State: value.State, Error: value.Error}
				},
			})
		}
	}
	return assembly, nil
}

func sourceHistoryCaps(live bool) protocol.AdapterCapabilities {
	return protocol.AdapterCapabilities{History: true, Live: live, LazyMessages: true,
		HistoryMode: "bounded", ActiveSend: "unsupported", Settings: []string{}}
}

func sourceStatus(state, reason string) AdapterStatus {
	value := AdapterStatus{State: state}
	if reason != "" {
		value.Error = &reason
	}
	return value
}

func codexReadIdentity(scope, id string) protocol.SessionAccess {
	data, _ := json.Marshal([3]string{"codex", scope, id})
	canonical := sha256.Sum256(data)
	binding := sha256.Sum256(append([]byte("codex-cli-read-only:"), data...))
	return protocol.SessionAccess{NativeScopeID: scope,
		CanonicalSessionRef: base64.RawURLEncoding.EncodeToString(canonical[:]),
		Binding:             base64.RawURLEncoding.EncodeToString(binding[:]), Capabilities: sourceHistoryCaps(false)}
}

// A peer's matching home string is insufficient. Derive both identities from
// actual native catalog file identity and current authentication bytes.
func verifySourceDaemon(sid string, home func(context.Context) (string, error), token UserWorkerToken) func(context.Context, codex.LocalDaemonIdentity) error {
	return func(ctx context.Context, peer codex.LocalDaemonIdentity) error {
		if peer.UserID != sid {
			return errors.New("Native daemon account identity is unavailable")
		}
		expected, err := codexSourceScope(sid, home, token)(ctx)
		if err != nil {
			return err
		}
		actual, err := codexSourceScope(sid, func(context.Context) (string, error) { return peer.CodexHome, nil }, token)(ctx)
		if err != nil || actual != expected {
			return errors.New("Native daemon store identity is unavailable")
		}
		return ctx.Err()
	}
}

// The generated file belongs to the existing private worker directory. Native
// config/auth/history are never changed; an invalid existing secret is refused.
func claudeModToken(path, workerToken string) (string, error) {
	value, err := readEntryText(path)
	if errors.Is(err, os.ErrNotExist) {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			return "", err
		}
		value = hex.EncodeToString(key[:])
		clear(key[:])
		if err := writeExclusive(path, []byte(value)); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if !tokenPattern.MatchString(value) || value == workerToken {
		return "", errors.New("Claude Mod requires its distinct private worker credential")
	}
	return value, nil
}
