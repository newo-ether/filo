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

// userSourceAssembly owns the optional selected-user source transports and
// clients. Independent native brokers retain accepted execution on close.
type userSourceAssembly struct {
	adapters      []Adapter
	cli           *codexcli.Adapter
	cliControl    *CLIControlledSessions
	claudeControl *ClaudeExecutorSessions
	bridge        http.Handler
	bridgeAddress string
}

func (a *userSourceAssembly) close() {
	if a == nil {
		return
	}
	if a.claudeControl != nil {
		a.claudeControl.Close()
	}
	if a.cliControl != nil {
		a.cliControl.Close()
	}
	if a.cli != nil {
		a.cli.Close()
	}
}

func assembleUserSources(config map[string]any, directory, token string, changed func(string, string)) (*userSourceAssembly, error) {
	ids, err := configuredSources(config)
	if err != nil {
		return nil, err
	}
	assembly := &userSourceAssembly{}
	if !hasConfiguredSource(ids, codexCLISource) && !hasConfiguredSource(ids, claudeCodeSource) && !hasConfiguredSource(ids, claudeDesktopSource) {
		return assembly, nil
	}
	sid, _ := config["desktopSid"].(string)
	if !systemServiceSIDPattern.MatchString(sid) {
		return nil, errors.New(systemServiceSIDRefusal)
	}
	if err := verifySourceUser(sid); err != nil {
		return nil, err
	}
	var claudeSurfaces []*claude.Adapter
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
			verifyMetadata := verifySourceDaemon(sid, home, StaticUserWorkerToken(token))
			metadata := newCLISourceMetadata(scope, func(ctx context.Context, nativeHome string) error {
				return verifyMetadata(ctx, codex.LocalDaemonIdentity{UserID: sid, CodexHome: nativeHome})
			})
			control, err := NewCLIControlledSessions(CLIControlledOptions{
				Directory: directory, Workspace: processWorkingDirectory(), Reader: cli, Metadata: metadata,
				History: history.NewNativeHistory(home), Scope: scope, NativeHome: home,
				Daemon: existingCLIDaemon(socket, sid, home, verifyMetadata),
				OnChange: func(session string) {
					if changed != nil {
						changed(id, session)
					}
				},
			})
			if err != nil {
				assembly.close()
				return nil, err
			}
			assembly.cliControl = control
			assembly.adapters = append(assembly.adapters, Adapter{ID: id, Family: "codex", Origin: "cli",
				Scope: scope, Sessions: &sourceReader{SessionAPI: control, scope: scope}, Capabilities: control.Capabilities(),
				Usage: control.Usage,
				Status: func() AdapterStatus {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					return control.Status(ctx)
				},
			})
		case claudeCodeSource, claudeDesktopSource:
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
			origin, publicOrigin := "terminal", "cli"
			if id == claudeDesktopSource {
				origin, publicOrigin = "desktop", "desktop"
			}
			bridge, err := claude.New(claude.Config{Token: secret, NativeScopeID: scope, Origin: origin,
				OnChange: func(session string) {
					if changed != nil {
						changed(id, session)
					}
				},
			})
			if err != nil {
				assembly.close()
				return nil, err
			}
			claudeSurfaces = append(claudeSurfaces, bridge)
			assembly.bridgeAddress = address
			var surface SessionAPI = bridge
			caps := claude.Capabilities()
			if id == claudeDesktopSource {
				caps = claude.DesktopCapabilities()
			}
			status := func() AdapterStatus {
				value := bridge.Status()
				return AdapterStatus{State: value.State, Error: value.Error}
			}
			if id == claudeCodeSource {
				control, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{
					Directory: filepath.Join(directory, "claude-code-tasks"), Workspace: processWorkingDirectory(),
					UploadDirectory: filepath.Join(directory, "uploads"),
					NativeScopeID:   scope, ReadOnly: bridge,
					OnChange: func(session string) {
						if changed != nil {
							changed(id, session)
						}
					},
				})
				if err != nil {
					assembly.close()
					return nil, err
				}
				assembly.claudeControl = control
				surface, caps = control, control.Capabilities()
				status = func() AdapterStatus {
					value := bridge.Status()
					if value.State == "connected" {
						return AdapterStatus{State: value.State, Error: value.Error}
					}
					return control.Status()
				}
			}
			assembly.adapters = append(assembly.adapters, Adapter{ID: id, Family: "claude", Origin: publicOrigin,
				NativeScopeID: scope, Sessions: surface, Capabilities: caps, Status: status,
			})
		}
	}
	if len(claudeSurfaces) > 0 {
		assembly.bridge = claude.SharedBridge(claudeSurfaces...)
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
