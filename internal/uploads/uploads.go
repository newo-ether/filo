// Package uploads stores raw attachments without decoding or interpreting them.
package uploads

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const MaxFileBytes int64 = 128 << 20
const MaxChunkBytes = 256 << 10
const MaxPending = 16
const MaxStoredBytes int64 = 2 << 30

var ErrInvalid = errors.New("invalid attachment upload")
var ErrBusy = errors.New("attachment storage limit reached")
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Metadata struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Path   string `json:"path,omitempty"`
}

type pending struct {
	meta    Metadata
	offset  int64
	touched time.Time
}
type Store struct {
	mu      sync.Mutex
	root    *os.Root
	path    string
	pending map[string]*pending
	used    int64
	now     func() time.Time
}

func New(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, path: path, pending: make(map[string]*pending), now: time.Now}
	entries, err := os.ReadDir(path)
	if err != nil {
		root.Close()
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".part") && idPattern.MatchString(strings.TrimSuffix(entry.Name(), ".part")) {
			// No native turn can refer to an incomplete upload. Restart discards only
			// these private fragments; committed original bytes remain available.
			if err := root.Remove(entry.Name()); err != nil {
				root.Close()
				return nil, err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			root.Close()
			return nil, err
		}
		s.used += info.Size()
	}
	return s, nil
}
func (s *Store) Close() error { return s.root.Close() }

func validName(name string) bool {
	if name == "" || len(name) > 240 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\:<>\"|?*\x00") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, r := range name {
		if r < 32 {
			return false
		}
	}
	stem := strings.ToUpper(strings.Split(name, ".")[0])
	return stem != "." && stem != ".." && stem != "CON" && stem != "PRN" && stem != "AUX" && stem != "NUL" && !regexp.MustCompile(`^(COM|LPT)[0-9]$`).MatchString(stem)
}

func (s *Store) Begin(meta Metadata) (Metadata, error) {
	if !validName(meta.Name) || meta.Size < 0 || meta.Size > MaxFileBytes || len(meta.MIME) > 128 || strings.HasPrefix(strings.ToLower(meta.MIME), "video/") {
		return Metadata{}, ErrInvalid
	}
	digest, err := hex.DecodeString(meta.SHA256)
	if err != nil || len(digest) != 32 {
		return Metadata{}, ErrInvalid
	}
	meta.SHA256 = strings.ToLower(meta.SHA256)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.pending {
		if s.now().Sub(p.touched) > time.Hour {
			s.root.Remove(id + ".part")
			s.used -= p.meta.Size
			delete(s.pending, id)
		}
	}
	if len(s.pending) >= MaxPending || meta.Size > MaxStoredBytes-s.used {
		return Metadata{}, ErrBusy
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return Metadata{}, err
	}
	meta.ID = hex.EncodeToString(random)
	meta.Path = ""
	file, err := s.root.OpenFile(meta.ID+".part", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Metadata{}, err
	}
	if err := file.Close(); err != nil {
		s.root.Remove(meta.ID + ".part")
		return Metadata{}, err
	}
	s.pending[meta.ID] = &pending{meta: meta, touched: s.now()}
	s.used += meta.Size
	return meta, nil
}

func (s *Store) Append(id string, offset int64, data []byte) (int64, error) {
	if !idPattern.MatchString(id) || len(data) > MaxChunkBytes || len(data) == 0 {
		return 0, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	if p == nil || offset != p.offset || int64(len(data)) > p.meta.Size-p.offset {
		return 0, ErrInvalid
	}
	file, err := s.root.OpenFile(id+".part", os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	n, err := file.WriteAt(data, offset)
	closeErr := file.Close()
	if err != nil || closeErr != nil || n != len(data) {
		s.root.Remove(id + ".part")
		s.used -= p.meta.Size
		delete(s.pending, id)
		return 0, errors.New("attachment chunk write failed")
	}
	p.offset += int64(n)
	p.touched = s.now()
	return p.offset, nil
}

func (s *Store) Complete(id string) (Metadata, error) {
	if !idPattern.MatchString(id) {
		return Metadata{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	if p == nil || p.offset != p.meta.Size {
		return Metadata{}, ErrInvalid
	}
	file, err := s.root.Open(id + ".part")
	if err != nil {
		return Metadata{}, err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		return Metadata{}, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != p.meta.SHA256 {
		return Metadata{}, fmt.Errorf("%w: checksum mismatch", ErrInvalid)
	}
	name := id + "-" + p.meta.Name
	if err := s.root.Rename(id+".part", name); err != nil {
		return Metadata{}, err
	}
	meta := p.meta
	meta.Path = filepath.Join(s.path, name)
	encoded, err := json.Marshal(meta)
	if err != nil {
		return Metadata{}, err
	}
	if err := s.root.WriteFile(id+".json", encoded, 0600); err != nil {
		// Keep the bytes, but never claim a receipt the service cannot verify later.
		delete(s.pending, id)
		return Metadata{}, err
	}
	delete(s.pending, id)
	s.used += int64(len(encoded))
	return meta, nil
}

func (s *Store) Resolve(ids []string) ([]Metadata, error) {
	if len(ids) > 16 {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Metadata, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !idPattern.MatchString(id) || seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		file, err := s.root.Open(id + ".json")
		if err != nil {
			return nil, ErrInvalid
		}
		data, err := io.ReadAll(io.LimitReader(file, 4097))
		file.Close()
		if err != nil || len(data) > 4096 {
			return nil, ErrInvalid
		}
		var meta Metadata
		if json.Unmarshal(data, &meta) != nil || meta.ID != id || !validName(meta.Name) {
			return nil, ErrInvalid
		}
		name := id + "-" + meta.Name
		info, err := s.root.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() != meta.Size {
			return nil, ErrInvalid
		}
		// Re-derive the path instead of trusting the persisted receipt or client.
		meta.Path = filepath.Join(s.path, name)
		result = append(result, meta)
	}
	return result, nil
}

func (s *Store) Cancel(id string) error {
	if !idPattern.MatchString(id) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pending[id]; p != nil {
		if err := s.root.Remove(id + ".part"); err != nil {
			return err
		}
		s.used -= p.meta.Size
		delete(s.pending, id)
	}
	return nil
}
