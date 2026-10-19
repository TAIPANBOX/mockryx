package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const cliTestAPIKey = "mockryx-fixture-api-key-0003"

// plainScenario expects a plain 200 and names no requires, so a gateway that
// answers 200 passes it and the run exits 0.
const plainScenario = `
name: api-key-plain
steps:
  - name: ask
    request:
      model: claude-haiku
      messages:
        - role: user
          content: hello
    expect:
      status: 200
`

// apiKeyGateway answers 200 and records the x-api-key of every request.
func apiKeyGateway(t *testing.T) (*httptest.Server, func() (requests int, keys []string)) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("x-api-key"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() (int, []string) {
		mu.Lock()
		defer mu.Unlock()
		return len(keys), append([]string(nil), keys...)
	}
}

// apiKeyFile writes content as a key file readable by its owner only.
func apiKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(data)
}

// clearAPIKeyEnv keeps the developer's shell from deciding these tests.
func clearAPIKeyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MOCKRYX_API_KEY", "")
	t.Setenv("MOCKRYX_API_KEY_FILE", "")
	t.Setenv("MOCKRYX_CLIENT_KEY", "")
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "")
}

// TestRunCommandSendsTheAPIKeyFromAFile: the key file named on the command
// line supplies the x-api-key every request carries.
func TestRunCommandSendsTheAPIKeyFromAFile(t *testing.T) {
	clearAPIKeyEnv(t)
	gw, keys := apiKeyGateway(t)
	dir := t.TempDir()
	writeScenario(t, dir, "plain.yaml", plainScenario)

	var err error
	captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, "--api-key-file", apiKeyFile(t, cliTestAPIKey), dir})
	})
	if err != nil {
		t.Fatalf("runRun: %v", err)
	}
	n, seen := keys()
	if n == 0 {
		t.Fatal("the gateway received nothing, so this measured nothing")
	}
	for _, k := range seen {
		if k != cliTestAPIKey {
			t.Errorf("x-api-key = %q, want the key from the file", k)
		}
	}
}

// TestRunCommandSendsTheAPIKeyFromTheKeyFileEnvironment: with no flag, the
// environment names the key file, and the key from it is sent.
func TestRunCommandSendsTheAPIKeyFromTheKeyFileEnvironment(t *testing.T) {
	clearAPIKeyEnv(t)
	t.Setenv("MOCKRYX_API_KEY_FILE", apiKeyFile(t, cliTestAPIKey))
	gw, keys := apiKeyGateway(t)
	dir := t.TempDir()
	writeScenario(t, dir, "plain.yaml", plainScenario)

	var err error
	captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, dir})
	})
	if err != nil {
		t.Fatalf("runRun: %v", err)
	}
	if n, seen := keys(); n == 0 || seen[0] != cliTestAPIKey {
		t.Errorf("gateway saw %d request(s) with keys %q, want the key from $MOCKRYX_API_KEY_FILE", n, seen)
	}
}

// TestRunCommandRefusesAnAPIKeyGivenTwiceBeforeSendingAnything: a key file and
// a key value at once is refused, naming both sources, before any request.
func TestRunCommandRefusesAnAPIKeyGivenTwiceBeforeSendingAnything(t *testing.T) {
	clearAPIKeyEnv(t)
	gw, keys := apiKeyGateway(t)
	dir := t.TempDir()
	writeScenario(t, dir, "plain.yaml", plainScenario)

	var err error
	captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, "--api-key-file", apiKeyFile(t, cliTestAPIKey), "--api-key", "other-value", dir})
	})
	if err == nil || exitCode(err) != exitUsage {
		t.Fatalf("err = %v, want a usage error: two API key sources must not be guessed between", err)
	}
	if !strings.Contains(err.Error(), "give the API key once") {
		t.Errorf("error %q does not say the key was given twice", err)
	}
	if n, _ := keys(); n != 0 {
		t.Errorf("the gateway received %d request(s), want 0", n)
	}
}

// TestAPIKeyFlagWarnsAndStillSendsTheKey: an x-api-key on the command line is
// still sent, and stderr names the flag on one line without printing the value.
func TestAPIKeyFlagWarnsAndStillSendsTheKey(t *testing.T) {
	clearAPIKeyEnv(t)
	const value = "flag-value-api-key-0004"
	gw, keys := apiKeyGateway(t)
	dir := t.TempDir()
	writeScenario(t, dir, "plain.yaml", plainScenario)

	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			err = runRun([]string{"--gateway", gw.URL, "--api-key", value, dir})
		})
	})
	if err != nil {
		t.Fatalf("runRun: %v", err)
	}
	if n, seen := keys(); n == 0 || seen[0] != value {
		t.Errorf("gateway saw %d request(s) with keys %q, want the flag value still sent", n, seen)
	}
	var named int
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "--api-key") {
			named++
		}
		if strings.Contains(line, value) {
			t.Errorf("stderr prints the key value: %q", line)
		}
	}
	if named != 1 {
		t.Errorf("stderr has %d line(s) naming --api-key, want exactly one warning:\n%s", named, stderr)
	}
}

// TestAPIKeyFromTheEnvironmentIsNotWarned: a key value from the environment
// stays off the command line and gets no warning. This is a negative control:
// it passes on the unfixed code too, because no warning existed.
func TestAPIKeyFromTheEnvironmentIsNotWarned(t *testing.T) {
	clearAPIKeyEnv(t)
	t.Setenv("MOCKRYX_API_KEY", cliTestAPIKey)
	gw, _ := apiKeyGateway(t)
	dir := t.TempDir()
	writeScenario(t, dir, "plain.yaml", plainScenario)

	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			err = runRun([]string{"--gateway", gw.URL, dir})
		})
	})
	if err != nil {
		t.Fatalf("runRun: %v", err)
	}
	if strings.Contains(stderr, "warning") {
		t.Errorf("an environment key produced a warning: %q", stderr)
	}
}
