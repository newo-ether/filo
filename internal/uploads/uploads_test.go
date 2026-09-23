package uploads

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func metadata(data []byte) Metadata {
	sum := sha256.Sum256(data)
	return Metadata{Name: "原始 image.bin", MIME: "application/octet-stream", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}
func store(t *testing.T) *Store {
	t.Helper()
	s, e := New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRawBytesSurviveChunkingCompletionAndRestart(t *testing.T) {
	s := store(t)
	data := append(bytes.Repeat([]byte{0, 255, 1, 128}, MaxChunkBytes/4), []byte("\x00原始\r\n")...)
	meta, err := s.Begin(metadata(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(meta.ID, 0, data[:MaxChunkBytes]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(meta.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete accepted: %v", err)
	}
	if _, err := s.Append(meta.ID, MaxChunkBytes, data[MaxChunkBytes:]); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.Complete(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(receipt.Path)
	if err != nil || !bytes.Equal(data, raw) {
		t.Fatalf("changed raw bytes: %v", err)
	}
	s.Close()
	restarted, err := New(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	resolved, err := restarted.Resolve([]string{meta.ID})
	if err != nil || len(resolved) != 1 || resolved[0] != receipt {
		t.Fatalf("receipt lost on restart: %v %v", resolved, err)
	}
}

func TestRejectsUnsafeNamesAndOversizedDeclarations(t *testing.T) {
	s := store(t)
	for _, name := range []string{"../secret", "..", "/root", "x\\y", "a:b", "CON", "nul.txt", "LPT9", "trailing.", "newline\nname"} {
		m := metadata(nil)
		m.Name = name
		if _, e := s.Begin(m); !errors.Is(e, ErrInvalid) {
			t.Fatalf("accepted %q", name)
		}
	}
	for _, size := range []int64{-1, MaxFileBytes + 1} {
		m := metadata(nil)
		m.Size = size
		if _, e := s.Begin(m); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid size accepted")
		}
	}
	m := metadata(nil)
	m.MIME = "video/mp4"
	if _, e := s.Begin(m); !errors.Is(e, ErrInvalid) {
		t.Fatal("video accepted")
	}
	if _, e := s.Resolve([]string{"../secret"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("traversal accepted")
	}
}

func TestOffsetsAndLimitsDoNotDuplicateBytes(t *testing.T) {
	s := store(t)
	data := []byte("abc")
	m, _ := s.Begin(metadata(data))
	if n, e := s.Append(m.ID, 0, data[:1]); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	for _, offset := range []int64{0, -1, 2} {
		if _, e := s.Append(m.ID, offset, data[:1]); !errors.Is(e, ErrInvalid) {
			t.Fatal("wrong offset accepted")
		}
	}
	if _, e := s.Append(m.ID, 1, make([]byte, MaxChunkBytes+1)); !errors.Is(e, ErrInvalid) {
		t.Fatal("large chunk accepted")
	}
	if _, e := s.Append(m.ID, 1, []byte("too long")); !errors.Is(e, ErrInvalid) {
		t.Fatal("declared size bypassed")
	}
	if _, e := s.Append(m.ID, 1, data[1:]); e != nil {
		t.Fatal(e)
	}
	receipt, e := s.Complete(m.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(receipt.Path)
	if string(raw) != "abc" {
		t.Fatal(string(raw))
	}
	if _, e := s.Resolve([]string{m.ID, m.ID}); !errors.Is(e, ErrInvalid) {
		t.Fatal("duplicate identity accepted")
	}
}

func TestChecksumMismatchNeverReturnsPath(t *testing.T) {
	s := store(t)
	m, _ := s.Begin(metadata([]byte("a")))
	s.Append(m.ID, 0, []byte("b"))
	receipt, err := s.Complete(m.ID)
	if !errors.Is(err, ErrInvalid) || receipt.Path != "" {
		t.Fatal(receipt, err)
	}
	if _, err := s.Resolve([]string{m.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatal("uncommitted accepted")
	}
	if err := s.Cancel(m.ID); err != nil {
		t.Fatal(err)
	}
	if s.used != 0 {
		t.Fatal("reservation leaked")
	}
}

func TestPendingBudgetAndExpiryRecoverWithoutDeletingCompleteFiles(t *testing.T) {
	s := store(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	m, _ := s.Begin(metadata(nil))
	completed, e := s.Complete(m.ID)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < MaxPending; i++ {
		if _, e := s.Begin(metadata(nil)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Begin(metadata(nil)); !errors.Is(e, ErrBusy) {
		t.Fatal("unbounded pending uploads")
	}
	now = now.Add(2 * time.Hour)
	if _, e := s.Begin(metadata(nil)); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(completed.Path); e != nil {
		t.Fatal("expiry deleted complete attachment", e)
	}
}

func TestRestartDropsOnlyOwnIncompleteFragments(t *testing.T) {
	s := store(t)
	m, _ := s.Begin(metadata([]byte("a")))
	s.Append(m.ID, 0, []byte("a"))
	other := filepath.Join(s.path, "owner.part")
	os.WriteFile(other, []byte("keep"), 0600)
	s.Close()
	next, e := New(s.path)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if _, e := os.Stat(filepath.Join(s.path, m.ID+".part")); !os.IsNotExist(e) {
		t.Fatal("fragment retained")
	}
	if raw, e := os.ReadFile(other); e != nil || string(raw) != "keep" {
		t.Fatal("unrelated file changed")
	}
	if _, e := next.Resolve([]string{strings.Repeat("0", 32)}); !errors.Is(e, ErrInvalid) {
		t.Fatal("fabricated identity accepted")
	}
}
