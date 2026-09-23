package service

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The two entry points of the service binary are exercised through the same
// functions the command line calls, so both entry test files share these
// helpers: one private credential, one reserved endpoint, one private entry
// directory and one bounded probe of the gateway that answers on it.

// entryTestToken is one 256 bit hexadecimal credential of a single entry point.
func entryTestToken(t *testing.T) string {
	t.Helper()
	secret, err := executorSecret()
	if err != nil {
		t.Fatalf("executorSecret: %v", err)
	}
	return secret
}

// entryBearerHeaders is the private credential header both gateways require.
func entryBearerHeaders(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// reserveLoopbackPort reserves one loopback port and releases it again, because
// a gateway under test binds its own endpoint.
func reserveLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("reserved address = %v", listener.Addr())
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return address.Port
}

// writeEntryFixtureFile writes one file of a private entry directory.
func writeEntryFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}

// assertEntryFixtureFile pins one private file byte for byte, which is how a
// refused configuration and a finished gateway both prove they never rewrote it.
func assertEntryFixtureFile(t *testing.T, path, expected string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if string(body) != expected {
		t.Fatalf("%s = %q, want %q", filepath.Base(path), body, expected)
	}
}

// readEntryInfo reads one /v1/info record. A gateway that is still starting
// refuses the connection, so an unanswered probe reports "no record yet" instead
// of failing the caller that is waiting for admission.
func readEntryInfo(client *http.Client, base string, headers map[string]string) (map[string]any, bool) {
	request, err := http.NewRequest(http.MethodGet, base+"/v1/info", nil)
	if err != nil {
		return nil, false
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, false
	}
	var info map[string]any
	if json.Unmarshal(body, &info) != nil {
		return nil, false
	}
	return info, true
}

// assertEntryRunning pins that one entry point is still serving, which is what
// an absent desktop or a refused credential must never change.
func assertEntryRunning(t *testing.T, done <-chan int) {
	t.Helper()
	select {
	case code := <-done:
		t.Fatalf("the entry point stopped with %d", code)
	default:
	}
}

// awaitEntryExit waits for one entry point to stop, which is the only proof that
// its shutdown path completed.
func awaitEntryExit(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(30 * time.Second):
		t.Fatalf("the entry point did not stop")
		return 0
	}
}
