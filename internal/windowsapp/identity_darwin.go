package windowsapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func NativeHome(ctx context.Context, uid string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if uid != strconv.Itoa(os.Geteuid()) || os.Geteuid() == 0 {
		return "", fmt.Errorf("FILO_APP_UID: selected user differs")
	}
	account, err := user.LookupId(uid)
	if err != nil {
		return "", fmt.Errorf("FILO_APP_HOME: %w", err)
	}
	return filepath.Join(account.HomeDir, ".codex"), nil
}

// Inspect discovers the actual peer executable and verifies the Apple trust chain, OpenAI team, and bundle ID.
// It does not depend on the application name, installation directory, or version.
func Inspect(ctx context.Context, pid uint32, uid string) (Application, error) {
	if _, err := NativeHome(ctx, uid); err != nil {
		return Application{}, err
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil {
		return Application{}, fmt.Errorf("FILO_APP_PROCESS: %w", err)
	}
	if process.Eproc.Ucred.Uid != uint32(os.Geteuid()) {
		return Application{}, fmt.Errorf("FILO_APP_OWNER: process UID differs")
	}
	output, err := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.FormatUint(uint64(pid), 10), "-o", "comm=").Output()
	if err != nil {
		return Application{}, fmt.Errorf("FILO_APP_PATH: %w", err)
	}
	executable := strings.TrimSpace(string(output))
	marker := ".app/Contents/MacOS/"
	index := strings.LastIndex(executable, marker)
	if !filepath.IsAbs(executable) || index < 0 {
		return Application{}, fmt.Errorf("FILO_APP_BUNDLE: peer is not an application main executable")
	}
	root := executable[:index+4]
	requirement := `=anchor apple generic and identifier "com.openai.codex" and certificate leaf[subject.OU] = "2DC432GLL2"`
	if output, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "-R", requirement, executable).CombinedOutput(); err != nil {
		return Application{}, fmt.Errorf("FILO_APP_SIGNATURE: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return Application{ProcessID: pid, UserSID: uid, Executable: executable, PackageRoot: root, PackageFullName: "com.openai.codex"}, nil
}
