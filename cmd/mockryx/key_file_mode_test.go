package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keyFileWithMode writes content as a key file with exactly mode.
func keyFileWithMode(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunCommandRefusesAClientKeyFileGroupOrOtherCanRead: a client key file that
// other users can read is refused as a usage error before any request is sent,
// and the refusal names the file and its mode. This is breaking for a 0644 client
// key file, which worked before this change.
func TestRunCommandRefusesAClientKeyFileGroupOrOtherCanRead(t *testing.T) {
	clearAPIKeyEnv(t)
	gw, count := keyedGateway(t, http.StatusOK)
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)
	path := keyFileWithMode(t, cliTestKey, 0o644)

	err := runRun([]string{"--gateway", gw.URL, "--client-key-file", path, dir})
	if err == nil || exitCode(err) != exitUsage {
		t.Fatalf("err = %v, want a usage error for a client key file other users can read", err)
	}
	if !strings.Contains(err.Error(), "0644") || !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file and its mode", err)
	}
	if n, _ := count(); n != 0 {
		t.Errorf("the gateway received %d request(s), want 0", n)
	}
}

// TestRunCommandRefusesAnAPIKeyFileGroupOrOtherCanRead: the same for the
// x-api-key file.
func TestRunCommandRefusesAnAPIKeyFileGroupOrOtherCanRead(t *testing.T) {
	clearAPIKeyEnv(t)
	gw, keys := apiKeyGateway(t)
	dir := t.TempDir()
	writeScenario(t, dir, "plain.yaml", plainScenario)
	path := keyFileWithMode(t, cliTestAPIKey, 0o640)

	err := runRun([]string{"--gateway", gw.URL, "--api-key-file", path, dir})
	if err == nil || exitCode(err) != exitUsage {
		t.Fatalf("err = %v, want a usage error for an API key file the group can read", err)
	}
	if !strings.Contains(err.Error(), "0640") {
		t.Errorf("error %q does not name the mode", err)
	}
	if n, _ := keys(); n != 0 {
		t.Errorf("the gateway received %d request(s), want 0", n)
	}
}
