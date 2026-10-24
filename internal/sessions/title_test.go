package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

type titleHistoryFixture struct {
	*historyFixture
	title protocol.Text
	err   error
}

func (h *titleHistoryFixture) Title(context.Context, string) (*protocol.Text, error) {
	return &h.title, h.err
}

func TestLiveTitleRefreshesWithoutReloadingHistoryAndMetadataFailureKeepsConversation(t *testing.T) {
	h := &titleHistoryFixture{historyFixture: &historyFixture{}, title: "Generated"}
	ipc := &readIPC{ipcFixture: &ipcFixture{owner: "original"}, snapshot: activeSnapshot}
	s := New(Options{IPC: ipc, History: h})
	ipc.f = s.f
	defer s.Close()
	read := func() protocol.ConversationPage {
		t.Helper()
		page, err := s.Read(context.Background(), "one", "", true, nil)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := read()
	if page.Runtime.Title == nil || *page.Runtime.Title != "Generated" {
		t.Fatal(page.Runtime)
	}
	h.title = "Renamed"
	page = read()
	if page.Runtime.Title == nil || *page.Runtime.Title != "Renamed" || h.reads.Load() != 1 {
		t.Fatal("stale title or unnecessary full history read", page.Runtime, h.reads.Load())
	}
	h.err = errors.New("metadata temporarily unavailable")
	page = read()
	if len(page.Messages) != 1 || page.Runtime.ActiveTurnID == nil {
		t.Fatal("optional title failure broke live read", page)
	}
}
