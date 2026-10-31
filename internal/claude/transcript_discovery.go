package claude

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

// Desktop can attach after the classic lifecycle event. Its exact native
// host/engine mapping qualifies a bounded lookup of that engine's transcript;
// it never admits another session, a CLI source or native control.
func (a *Adapter) discoverDesktopTranscript(ctx context.Context, id string) {
	a.mu.Lock()
	r, err := a.findLocked(id)
	if err != nil || r.origin != "desktop" || r.transcript != "" {
		a.mu.Unlock()
		return
	}
	nonce := r.nonce
	a.mu.Unlock()
	identity := resolveDesktopIdentity(ctx, id)
	if identity == nil {
		return
	}
	path := desktopTranscriptPath(ctx, id)
	if path == "" || !identity.unchanged() || ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err = a.findLocked(id)
	if err == nil && r.nonce == nonce && r.origin == "desktop" && r.transcript == "" {
		r.transcript = path
	}
}

func desktopTranscriptPath(ctx context.Context, id string) string {
	home := os.Getenv("CLAUDE_CONFIG_DIR")
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(user, ".claude")
	}
	root := filepath.Join(home, "projects")
	directory, err := os.Open(root)
	if err != nil {
		return ""
	}
	projects, err := directory.ReadDir(1025)
	directory.Close()
	if err != nil && err != io.EOF || len(projects) > 1024 {
		return ""
	}
	var found string
	for _, project := range projects {
		if ctx.Err() != nil {
			return ""
		}
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 {
			continue
		}
		path, err := scopedTranscriptPath(filepath.Join(root, project.Name(), id+".jsonl"), id)
		if err != nil {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if found != "" {
			return "" // Multiple project copies cannot establish one native store.
		}
		found = path
	}
	return found
}
