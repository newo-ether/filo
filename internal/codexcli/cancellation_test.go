package codexcli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestScopeCallbacksPreserveTypedCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			f := &historyFixture{}
			a := newFixture(t, f, func(context.Context) (string, error) { return "", fmt.Errorf("scope: %w", cause) })
			if _, err := a.List(context.Background(), ""); !errors.Is(err, cause) {
				t.Fatal("scope cancellation lost its type", err)
			}
			if f.calls.Load() != 0 {
				t.Fatal("cancelled scope read history")
			}
		})
	}
}

func TestCancelledHistoryResultsNeverEscapeAsUnavailableOrPartialSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &historyFixture{}
	a := newFixture(t, f, nil)
	f.read = func(string, string, bool, []string) (protocol.ConversationPage, error) {
		cancel()
		return protocol.ConversationPage{Messages: []protocol.Message{}}, nil
	}
	if page, err := a.Read(ctx, firstID, "", false, nil); !errors.Is(err, context.Canceled) || page.Access != nil {
		t.Fatal("cancelled history result escaped", page, err)
	}
	if err := a.sameScope(ctx, "verified-scope"); !errors.Is(err, context.Canceled) {
		t.Fatal("post-read scope lost cancellation", err)
	}
}
