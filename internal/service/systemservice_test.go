package service

import (
	"github.com/newo-ether/conch/encryptedhttp"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The desktop IPC gateway is the service the original desktop launches as one
// Windows service account. These tests run the entry function in this process.
// The first pins the gate every malformed private directory stops at, and the
// second pins that an absent original desktop leaves a reachable gateway instead
// of a restart loop, which is what "IPC priority and no desktop control" means
// for the service entry point.

// TestSystemServiceEntryRejectsMalformedPrivateConfigWithoutChangingIt pins the
// first gate of the service: a configuration that cannot be decoded, or one that
// names a published endpoint, stops the service before any port, credential or
// desktop resource is touched, and the file it read stays exactly as it was.
func TestSystemServiceEntryRejectsMalformedPrivateConfigWithoutChangingIt(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, entryConfigName)
	for _, content := range []string{"{", `{"bind":"0.0.0.0","port":7435}`} {
		writeEntryFixtureFile(t, path, content)
		var diagnostic strings.Builder
		if code := runSystemService(systemServiceOptions{Directory: directory, Output: io.Discard, Error: &diagnostic}); code != 1 {
			t.Fatalf("a rejected configuration exited with %d", code)
		}
		if !strings.HasPrefix(diagnostic.String(), systemServiceDiagnosticPrefix) {
			t.Fatalf("diagnostic = %q", diagnostic.String())
		}
		assertEntryFixtureFile(t, path, content)
	}
}

// TestSystemServiceGatewayStaysReachableWithoutTheOriginalDesktop pins the
// service contract when the original desktop is absent: the gateway admits its
// endpoint, reports the missing identity on one authenticated request, keeps
// answering every later request, and still shuts down on request. Neither the
// private configuration nor the unprepared helper credential is rewritten.
func TestSystemServiceGatewayStaysReachableWithoutTheOriginalDesktop(t *testing.T) {
	directory := t.TempDir()
	port := reserveLoopbackPort(t)
	config := Marshal(map[string]any{
		"bind":            "127.0.0.1",
		"port":            port,
		"desktopSid":      "S-1-5-21-1-2-3-1001",
		"workerUrl":       "http://127.0.0.1:1",
		"workerTokenPath": filepath.Join(directory, "not-yet-prepared-token"),
	})
	writeEntryFixtureFile(t, filepath.Join(directory, entryConfigName), string(config))
	writeEntryFixtureFile(t, filepath.Join(directory, entryTokenName), testToken)

	// A pipe nobody listens on keeps this gateway away from the original
	// desktop of the machine that runs the test.
	var output strings.Builder
	done := make(chan int, 1)
	go func() {
		done <- runSystemService(systemServiceOptions{
			Directory: directory,
			PipePath:  `\\.\pipe\filo-absent-` + strconv.Itoa(port),
			Output:    &output,
			Error:     io.Discard,
		})
	}()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	headers := authHeaders()
	client := &http.Client{Timeout: time.Second, Transport: &encryptedhttp.Transport{Key: []byte(testToken)}}
	var info map[string]any
	for attempt := 0; attempt < 80; attempt++ {
		// Desktop absence must never terminate the service.
		assertEntryRunning(t, done)
		if record, ok := readEntryInfo(client, base, headers); ok {
			info = record
			if desktopState(record) == "unavailable" {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if desktopState(info) != "unavailable" {
		t.Fatalf("desktop = %v", info["desktop"])
	}
	if message := desktopError(info); !strings.Contains(message, "identity could not be verified") {
		t.Fatalf("desktop error = %q", message)
	}
	if response := get(t, base+"/v1/info", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	assertEntryRunning(t, done)
	assertEntryFixtureFile(t, filepath.Join(directory, entryConfigName), string(config))

	stopped, stopError := client.Post(base+"/v1/shutdown", "application/json", strings.NewReader("{}"))
	if stopError != nil {
		t.Fatal(stopError)
	}
	if stopped.StatusCode != http.StatusOK {
		t.Fatalf("shutdown status = %d", stopped.StatusCode)
	}
	stopped.Body.Close()
	if code := awaitEntryExit(t, done); code != 0 {
		t.Fatalf("a requested shutdown exited with %d", code)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	ready := `{"service":"Filo","device":"` + hostname + `","mode":"desktop-ipc","port":` + strconv.Itoa(port) + "}\n"
	if output.String() != ready {
		t.Fatalf("ready record = %q", output.String())
	}
}

// desktopState reads the reported desktop state of one info record.
func desktopState(info map[string]any) string {
	desktop, _ := info["desktop"].(map[string]any)
	state, _ := desktop["state"].(string)
	return state
}

// desktopError reads the reported desktop error of one info record.
func desktopError(info map[string]any) string {
	desktop, _ := info["desktop"].(map[string]any)
	message, _ := desktop["error"].(string)
	return message
}
