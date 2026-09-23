package sessions

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

// createdFixture is the separately verified created scope. It records every
// dispatch so a test can prove which call reached the auxiliary transport.
type createdFixture struct {
	own      bool
	calls    atomic.Int32
	released atomic.Int32

	mu       sync.Mutex
	aux      []string
	owns     func(string) (bool, error)
	statuses func([]string) ([]protocol.SessionStatus, error)
}

var _ CreatedReader = (*createdFixture)(nil)

func (c *createdFixture) record(entry string) {
	c.mu.Lock()
	c.aux = append(c.aux, entry)
	c.mu.Unlock()
}

func (c *createdFixture) entries() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.aux...)
}

func (c *createdFixture) Owns(_ context.Context, id string) (bool, error) {
	c.calls.Add(1)
	if c.owns != nil {
		return c.owns(id)
	}
	return c.own, nil
}
func (c *createdFixture) Read(_ context.Context, id, cursor string, activity bool) (protocol.ConversationPage, error) {
	c.calls.Add(1)
	c.record("read " + id + " " + cursor + " " + strconv.FormatBool(activity))
	return protocol.ConversationPage{Messages: []protocol.Message{historical("created", "created", 0)}}, nil
}
func (c *createdFixture) Attach(_ context.Context, id string) (func(), error) {
	c.calls.Add(1)
	c.record("attach " + id)
	return func() { c.released.Add(1) }, nil
}
func (c *createdFixture) Statuses(_ context.Context, ids []string) ([]protocol.SessionStatus, error) {
	c.calls.Add(1)
	c.record("statuses " + strings.Join(ids, ","))
	if c.statuses != nil {
		return c.statuses(ids)
	}
	return nil, errors.New("helper unavailable")
}
func (c *createdFixture) Send(_ context.Context, id, text, clientID string) (protocol.SendReceipt, error) {
	c.calls.Add(1)
	c.record("send " + id + " " + text + " " + clientID)
	return protocol.SendReceipt{TurnID: "scoped-turn", ClientID: clientID}, nil
}
func (c *createdFixture) Stop(_ context.Context, id, turnID string) error {
	c.calls.Add(1)
	c.record("stop " + id + " " + turnID)
	return nil
}
func (c *createdFixture) UpdateSettings(_ context.Context, id string, settings protocol.SessionSettings) error {
	c.calls.Add(1)
	entry := "settings " + id
	if settings.Model.Known {
		entry += " model=" + settings.Model.Value
	}
	if settings.Effort.Known {
		entry += " effort=" + settings.Effort.Value
	}
	if settings.ServiceTier.Known && settings.ServiceTier.Value != nil {
		entry += " serviceTier=" + *settings.ServiceTier.Value
	}
	c.record(entry)
	return nil
}

func TestMissingOwnerDoesNotAdmitKnownOriginalTaskToCreatedScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &historyFixture{}
		s, ipc := readFixture(h)
		defer s.Close()
		created := &createdFixture{own: true}
		s.options.Created = created
		release, err := s.Attach(context.Background(), "native")
		if err != nil {
			t.Fatal(err)
		}
		ipc.mu.Lock()
		ipc.owner = ""
		ipc.mu.Unlock()
		page, err := s.Read(context.Background(), "native", "", false, nil)
		if err != nil || len(page.Messages) != 0 || page.Runtime.Status != "notLoaded" || created.calls.Load() != 0 {
			t.Fatal("Known original task escaped its owner", page, err)
		}
		release()
		release()
		if _, err := s.Read(context.Background(), "created", "", false, nil); err != nil || created.calls.Load() != 2 {
			t.Fatal(err)
		}
		release, err = s.Attach(context.Background(), "created")
		if err != nil {
			t.Fatal(err)
		}
		release()
		if created.released.Load() != 1 {
			t.Fatal("Scoped reader cleanup was lost")
		}
		values, err := s.Statuses(context.Background(), []string{"native", "created"})
		if err != nil || len(values) != 2 || values[0].Status != nil || values[1].Status != nil {
			t.Fatal("Helper failure lost ordered unknown rows", values, err)
		}
	})
}

func TestUnownedOrdinaryHistoryAttachmentNeverStartsFollowing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &historyFixture{}
		s, ipc := readFixture(h)
		defer s.Close()
		ipc.owner = ""
		release, err := s.Attach(context.Background(), "history")
		if err != nil {
			t.Fatal(err)
		}
		release()
		if h.reads.Load() != 1 || ipc.count(true) != 0 {
			t.Fatal("Browsing acquired a follower")
		}
		if _, err := s.Statuses(context.Background(), make([]string, 13)); err == nil {
			t.Fatal("Visible status limit ignored")
		}
	})
}
