package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const cliTestKey = "mockryx-fixture-client-key-0002"

// keyedGateway answers 200 only to a caller presenting cliTestKey and records
// the agent id each request carried. It echoes the key back in a header, so a
// report that leaked it would show it.
func keyedGateway(t *testing.T, status int) (*httptest.Server, func() (requests int, agents []string)) {
	t.Helper()
	var mu sync.Mutex
	var agents []string
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		agents = append(agents, r.Header.Get("x-fuse-agent-id"))
		mu.Unlock()
		if r.Header.Get("x-fuse-key") != cliTestKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("x-echo", cliTestKey)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() (int, []string) {
		mu.Lock()
		defer mu.Unlock()
		return n, append([]string(nil), agents...)
	}
}

func keyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "client-key")
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// okScenario expects the 200 a keyed gateway answers once the key is accepted.
const okScenario = `
name: test-keyed-ok
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

func TestRunCommandSendsTheClientKeyFromAFile(t *testing.T) {
	t.Setenv("MOCKRYX_CLIENT_KEY", "")
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "")
	gw, _ := keyedGateway(t, http.StatusOK)
	dir := t.TempDir()
	writeScenario(t, dir, "ok.yaml", okScenario)

	var err error
	captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, "--client-key-file", keyFile(t, cliTestKey), dir})
	})
	if err != nil {
		t.Fatalf("runRun with the right key: %v (a 401 here means the key was not sent)", err)
	}
}

func TestRunCommandSendsTheClientKeyFromTheEnvironmentValue(t *testing.T) {
	t.Setenv("MOCKRYX_CLIENT_KEY", cliTestKey)
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "")
	gw, _ := keyedGateway(t, http.StatusOK)
	dir := t.TempDir()
	writeScenario(t, dir, "ok.yaml", okScenario)

	var err error
	captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, dir})
	})
	if err != nil {
		t.Fatalf("runRun with the key in $MOCKRYX_CLIENT_KEY: %v", err)
	}
}

func TestRunCommandRefusesTwoKeySourcesBeforeSendingAnything(t *testing.T) {
	t.Setenv("MOCKRYX_CLIENT_KEY", cliTestKey)
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "")
	gw, count := keyedGateway(t, http.StatusOK)
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	err := runRun([]string{"--gateway", gw.URL, "--client-key-file", keyFile(t, cliTestKey), dir})
	if err == nil || exitCode(err) != exitUsage {
		t.Fatalf("err = %v, want a usage error for a key given two ways", err)
	}
	if n, _ := count(); n != 0 {
		t.Errorf("the gateway received %d requests, want 0", n)
	}
}

func TestRunCommandRefusesAnUnreadableKeyFileBeforeSendingAnything(t *testing.T) {
	t.Setenv("MOCKRYX_CLIENT_KEY", "")
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "")
	gw, count := keyedGateway(t, http.StatusOK)
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)
	missing := filepath.Join(t.TempDir(), "no-such-key")

	err := runRun([]string{"--gateway", gw.URL, "--client-key-file", missing, dir})
	if err == nil || exitCode(err) != exitUsage {
		t.Fatalf("err = %v, want a usage error for an unreadable key file", err)
	}
	if !strings.Contains(err.Error(), "no-such-key") {
		t.Errorf("error = %q, want it to name the file", err)
	}
	if n, _ := count(); n != 0 {
		t.Errorf("the gateway received %d requests, want 0", n)
	}
}

func TestRunCommandHasNoFlagThatTakesTheKeyValue(t *testing.T) {
	t.Setenv("MOCKRYX_CLIENT_KEY", "")
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "")
	gw, count := keyedGateway(t, http.StatusOK)
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	err := runRun([]string{"--gateway", gw.URL, "--client-key", cliTestKey, dir})
	if err == nil || exitCode(err) != exitUsage {
		t.Fatalf("err = %v, want a usage error: the key value must never be a flag, so it stays off argv", err)
	}
	if n, _ := count(); n != 0 {
		t.Errorf("the gateway received %d requests, want 0", n)
	}
}

func TestRunCommandAgentIDFromTheEnvironmentReplacesTheScenarioAgent(t *testing.T) {
	t.Setenv("MOCKRYX_CLIENT_KEY", cliTestKey)
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "")
	t.Setenv("MOCKRYX_AGENT_ID", "agent://td/agents/mockryx")
	gw, agents := keyedGateway(t, http.StatusOK)
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario+"\n# the agent id below is the scenario's own, overridden by the environment\n")
	writeScenario(t, dir, "own.yaml", `
name: own-agent
steps:
  - name: m
    request:
      model: claude-haiku
      messages:
        - role: user
          content: hello
    headers:
      agent_id: agent://mockryx.local/rehearsal/own
    expect:
      status: 200
`)

	captureStdout(t, func() { _ = runRun([]string{"--gateway", gw.URL, dir}) })

	_, seen := agents()
	for _, a := range seen {
		if a != "agent://td/agents/mockryx" {
			t.Errorf("gateway saw agent id %q, want the environment's id on every request", a)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the gateway received nothing, so this measured nothing")
	}
}

func TestRunCommandRefusedCredentialIsAFindingNotASkip(t *testing.T) {
	t.Setenv("MOCKRYX_CLIENT_KEY", "")
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "")
	gw, _ := keyedGateway(t, http.StatusOK)
	dir := t.TempDir()
	writeScenario(t, dir, "wardryx.yaml", wardryxScenario)

	var err error
	out := captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, dir})
	})
	if exitCode(err) != exitFindings {
		t.Fatalf("exit = %d, want %d: a refused key on a scenario that declares requires must not exit 0 as not configured\n%s", exitCode(err), exitFindings, out)
	}
	if !strings.Contains(out, "the gateway refused the drill's credential") {
		t.Errorf("report does not say the credential was refused:\n%s", out)
	}
}

func TestRunCommandKeyNeverReachesTheReport(t *testing.T) {
	t.Setenv("MOCKRYX_CLIENT_KEY", "")
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "")
	gw, _ := keyedGateway(t, http.StatusOK)
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario) // expects 402, gets 200: a Finding
	saved := filepath.Join(t.TempDir(), "report.json")

	var err error
	out := captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, "--client-key-file", keyFile(t, cliTestKey), "--format", "json", "--save", saved, dir})
	})
	if exitCode(err) != exitFindings {
		t.Fatalf("exit = %d, want a finding so the report carries a result to inspect", exitCode(err))
	}
	onDisk, readErr := os.ReadFile(saved)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !json.Valid(onDisk) {
		t.Fatalf("saved report is not JSON: %s", onDisk)
	}
	for name, body := range map[string]string{"stdout": out, "saved report": string(onDisk)} {
		if strings.Contains(body, cliTestKey) {
			t.Errorf("the client key appears in the %s:\n%s", name, body)
		}
	}
}
