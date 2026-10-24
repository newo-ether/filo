package claude

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestNativeUploadDirectoryKeepsDefaultPermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "uploads with spaces")
	args, err := nativeArguments(NativeOptions{UploadDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	index := slices.Index(args, "--add-dir")
	permission := slices.Index(args, "--permission-mode")
	if index < 0 || args[index+1] != directory || permission < 0 || args[permission+1] != "default" {
		t.Fatalf("unsafe native arguments: %q", args)
	}
	for _, invalid := range []string{"relative", directory + "\nother"} {
		if _, err := nativeArguments(NativeOptions{UploadDirectory: invalid}); err == nil {
			t.Fatalf("invalid upload directory admitted: %q", invalid)
		}
	}
	s, _ := nativeFixture(t)
	if s.Access().Capabilities.Uploads {
		t.Fatal("old executor gained upload permission")
	}
	s.options.UploadDirectory = directory
	if !s.Access().Capabilities.Uploads {
		t.Fatal("idle upload-enabled executor lost attachments")
	}
	s.fenced = true
	if s.Access().Capabilities.Uploads {
		t.Fatal("uncertain executor retained uploads")
	}
}
