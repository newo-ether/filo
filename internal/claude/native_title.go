package claude

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/newo-ether/filo/internal/protocol"
)

// Native metadata is optional. Its failure must never break live conversation
// reads. Read only bounded title records; never recover or rewrite transcripts.
func (a *Adapter) refreshTitle(ctx context.Context, id string) {
	a.mu.Lock()
	r, err := a.findLocked(id)
	if err != nil {
		a.mu.Unlock()
		return
	}
	nonce, origin, transcript, metadata := r.nonce, r.origin, r.transcript, r.metadata
	a.mu.Unlock()
	var title *protocol.Text
	if origin == "desktop" {
		if metadata == "" {
			metadata = desktopMetadataPath(ctx, id)
		}
		title = desktopMetadataTitle(metadata, id)
	} else if transcript != "" {
		title = transcriptTitle(transcript, id)
	}
	if ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err = a.findLocked(id)
	if err != nil || r.nonce != nonce {
		return
	}
	if title != nil {
		r.title = title
	}
	if metadata != "" {
		r.metadata = metadata
	}
}

func readTitleFile(path string, limit int64) []byte {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil
	}
	return data
}

func desktopMetadataTitle(path, id string) *protocol.Text {
	if path == "" {
		return nil
	}
	var value struct {
		ID    string         `json:"cliSessionId"`
		Title *protocol.Text `json:"title"`
	}
	if json.Unmarshal(readTitleFile(path, 256<<10), &value) != nil || value.ID != id || value.Title == nil || len(*value.Title) > 4096 {
		return nil
	}
	return value.Title
}

func desktopMetadataPath(ctx context.Context, id string) string {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return ""
	}
	root := filepath.Join(local, "Claude-3p", "claude-code-sessions")
	users, err := os.ReadDir(root)
	if err != nil || len(users) > 128 {
		return ""
	}
	inspected := 0
	for _, user := range users {
		if !user.IsDir() || user.Type()&os.ModeSymlink != 0 {
			continue
		}
		accounts, _ := os.ReadDir(filepath.Join(root, user.Name()))
		if len(accounts) > 128 {
			continue
		}
		for _, account := range accounts {
			if !account.IsDir() || account.Type()&os.ModeSymlink != 0 {
				continue
			}
			directory := filepath.Join(root, user.Name(), account.Name())
			files, _ := os.ReadDir(directory)
			if len(files) > 1024 {
				continue
			}
			for _, file := range files {
				if ctx.Err() != nil || inspected >= 1024 {
					return ""
				}
				if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
					continue
				}
				inspected++
				path := filepath.Join(directory, file.Name())
				if desktopMetadataTitle(path, id) != nil {
					return path
				}
			}
		}
	}
	return ""
}

func transcriptTitle(path, id string) *protocol.Text {
	if !filepath.IsAbs(path) || filepath.Base(path) != id+".jsonl" {
		return nil
	}
	home := os.Getenv("CLAUDE_CONFIG_DIR")
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		home = filepath.Join(user, ".claude")
	}
	relative, err := filepath.Rel(filepath.Join(home, "projects"), path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	// Read both the initial generated-title records and the latest rename.
	// Each window is bounded, including when attaching to a large transcript.
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return nil
	}
	offset := max(int64(0), info.Size()-(64<<10))
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return nil
		}
		tail, err := io.ReadAll(io.LimitReader(file, 64<<10))
		if err != nil {
			return nil
		}
		if end := strings.LastIndexByte(string(data), '\n'); end >= 0 {
			data = data[:end+1]
		} else {
			data = nil
		}
		if start := strings.IndexByte(string(tail), '\n'); start >= 0 {
			data = append(data, tail[start+1:]...)
		}
	}
	lines := strings.Split(string(data), "\n")
	var title *protocol.Text
	for _, line := range lines {
		var row struct {
			Type    string         `json:"type"`
			Session string         `json:"sessionId"`
			Custom  *protocol.Text `json:"customTitle"`
			Summary *protocol.Text `json:"summary"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.Session != "" && row.Session != id {
			continue
		}
		value := row.Custom
		if row.Type == "summary" {
			value = row.Summary
		}
		if (row.Type == "custom-title" || row.Type == "summary") && value != nil && len(*value) <= 4096 {
			title = value
		}
	}
	return title
}
