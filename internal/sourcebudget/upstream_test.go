package sourcebudget

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVendoredConchMatchesRecordedUpstreamBytes(t *testing.T) {
	root := filepath.Join(moduleRoot(t), "thirdparty", "conch")
	data, err := os.ReadFile(filepath.Join(root, "UPSTREAM.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Revision string
		Files    []struct {
			Path   string
			SHA256 string
		}
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Revision) != 40 || len(manifest.Files) == 0 {
		t.Fatal("missing upstream identity")
	}
	recorded := map[string]bool{}
	for _, entry := range manifest.Files {
		if !filepath.IsLocal(entry.Path) || recorded[entry.Path] {
			t.Fatalf("invalid upstream path %q", entry.Path)
		}
		recorded[entry.Path] = true
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != entry.SHA256 {
			t.Fatalf("upstream bytes changed: %s", entry.Path)
		}
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !recorded[filepath.ToSlash(relative)] {
				t.Errorf("unrecorded upstream source: %s", relative)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
