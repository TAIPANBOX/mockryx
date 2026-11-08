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

// The two keys differ on purpose: invariant 13 is about the gateway's key never
// reaching the broker, and the broker's key never reaching the gateway.
const cliBrokerKey = "mockryx-fixture-broker-key-0004"

type seen struct{ method, path, key string }

// keyedBroker is a broker stand-in with a door: a POST is answered with the
// secret-scan block only when it carries cliBrokerKey, and with a 401 that is not
// JSON-RPC otherwise, as TokenFuse's broker does with TOKENFUSE_MCP_KEYS set. It
// echoes the key it was shown, so a report that leaked it would show it, and it
// records every request.
func keyedBroker(t *testing.T) (*httptest.Server, func() []seen) {
	t.Helper()
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := r.Header.Get("x-fuse-key")
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.Path, k})
		mu.Unlock()
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.Header().Set("x-echo", k)
		w.Header().Set("content-type", "application/json")
		if k != cliBrokerKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"unauthorized","reason":"credential required ` + k + `"}}`))
			return
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32002, "message": "blocked: raw secret in tool arguments"}})
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), got...)
	}
}

// recordingGateway records the x-fuse-key of every request it receives.
func recordingGateway(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("x-fuse-key"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), keys...)
	}
}

func brokerKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp-broker-key")
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearBrokerKeyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MOCKRYX_MCP_BROKER", "MOCKRYX_MCP_BROKER_KEY", "MOCKRYX_MCP_BROKER_KEY_FILE", "MOCKRYX_CLIENT_KEY", "MOCKRYX_CLIENT_KEY_FILE", "MOCKRYX_API_KEY", "MOCKRYX_API_KEY_FILE", "MOCKRYX_EVENTS_PATH"} {
		t.Setenv(k, "")
	}
}

func TestRunCommandSendsTheBrokerKeyFromAFileToTheBrokerOnly(t *testing.T) {
	clearBrokerKeyEnv(t)
	gw, gwKeys := recordingGateway(t)
	broker, brokerSeen := keyedBroker(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)

	var err error
	out := captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL,
			"--client-key-file", keyFile(t, cliTestKey), "--mcp-broker-key-file", brokerKeyFile(t, cliBrokerKey), dir})
	})
	if err != nil {
		t.Fatalf("runRun: %v\n%s", err, out)
	}
	posts := 0
	for _, r := range brokerSeen() {
		if r.method != http.MethodPost {
			if r.key != "" {
				t.Errorf("the %s %s probe carried x-fuse-key %q; the key goes on the JSON-RPC call only", r.method, r.path, r.key)
			}
			continue
		}
		posts++
		if r.key != cliBrokerKey {
			t.Errorf("a JSON-RPC call carried x-fuse-key %q, want the broker key", r.key)
		}
	}
	if posts == 0 {
		t.Fatal("no JSON-RPC call reached the broker, so this measured nothing")
	}
	for _, k := range gwKeys() {
		if k == cliBrokerKey {
			t.Error("the gateway received the broker key")
		}
	}
}

func TestRunCommandSendsTheBrokerKeyFromTheKeyFileEnvironment(t *testing.T) {
	clearBrokerKeyEnv(t)
	gw, _ := recordingGateway(t)
	broker, _ := keyedBroker(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)
	t.Setenv("MOCKRYX_MCP_BROKER_KEY_FILE", brokerKeyFile(t, cliBrokerKey))

	var err error
	out := captureStdout(t, func() { err = runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, dir}) })
	if err != nil {
		t.Fatalf("runRun: %v\n%s", err, out)
	}
}

func TestRunCommandSendsTheBrokerKeyFromTheEnvironmentValue(t *testing.T) {
	clearBrokerKeyEnv(t)
	gw, _ := recordingGateway(t)
	broker, _ := keyedBroker(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)
	t.Setenv("MOCKRYX_MCP_BROKER_KEY", cliBrokerKey)

	var err error
	out := captureStdout(t, func() { err = runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, dir}) })
	if err != nil {
		t.Fatalf("runRun: %v\n%s", err, out)
	}
}

// TestRunCommandWithNoBrokerKeyBehavesAsItDid: without the setting the broker is
// shown no key, a keyed broker answers its 401, and that stays the hard Finding it
// was (exit 1, not a skip and not a usage error).
func TestRunCommandWithNoBrokerKeyBehavesAsItDid(t *testing.T) {
	clearBrokerKeyEnv(t)
	gw, _ := recordingGateway(t)
	broker, brokerSeen := keyedBroker(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)

	var err error
	out := captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, "--client-key-file", keyFile(t, cliTestKey), dir})
	})
	if exitCode(err) != exitFindings {
		t.Fatalf("exit = %d, want %d\n%s", exitCode(err), exitFindings, out)
	}
	if !strings.Contains(out, "not a JSON-RPC reply") {
		t.Errorf("report does not say the broker's answer was not JSON-RPC:\n%s", out)
	}
	for _, r := range brokerSeen() {
		if r.key != "" {
			t.Errorf("the broker was shown x-fuse-key %q with no broker key configured", r.key)
		}
	}
}

func TestRunCommandRefusesTwoBrokerKeySourcesBeforeSendingAnything(t *testing.T) {
	clearBrokerKeyEnv(t)
	gw, gwKeys := recordingGateway(t)
	broker, brokerSeen := keyedBroker(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)
	t.Setenv("MOCKRYX_MCP_BROKER_KEY", cliBrokerKey)

	err := runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, "--mcp-broker-key-file", brokerKeyFile(t, cliBrokerKey), dir})
	if err == nil || exitCode(err) != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if len(gwKeys()) != 0 || len(brokerSeen()) != 0 {
		t.Errorf("requests were sent before the refusal: gateway %d, broker %d", len(gwKeys()), len(brokerSeen()))
	}
}

func TestRunCommandRefusesAnUnreadableBrokerKeyFileBeforeSendingAnything(t *testing.T) {
	clearBrokerKeyEnv(t)
	gw, gwKeys := recordingGateway(t)
	broker, brokerSeen := keyedBroker(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)
	missing := filepath.Join(t.TempDir(), "not-there")

	err := runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, "--mcp-broker-key-file", missing, dir})
	if err == nil || exitCode(err) != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if len(gwKeys()) != 0 || len(brokerSeen()) != 0 {
		t.Errorf("requests were sent before the refusal: gateway %d, broker %d", len(gwKeys()), len(brokerSeen()))
	}
}

// TestRunCommandRefusesTheSameKeyForTheGatewayAndTheBroker: one secret given as
// both keys would hand the gateway's credential to the broker, which is what
// invariant 13 exists to prevent. It is refused before anything is sent.
func TestRunCommandRefusesTheSameKeyForTheGatewayAndTheBroker(t *testing.T) {
	clearBrokerKeyEnv(t)
	gw, gwKeys := recordingGateway(t)
	broker, brokerSeen := keyedBroker(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)

	err := runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL,
		"--client-key-file", keyFile(t, cliBrokerKey), "--mcp-broker-key-file", brokerKeyFile(t, cliBrokerKey), dir})
	if err == nil || exitCode(err) != exitUsage {
		t.Fatalf("err = %v, want a usage error: one key for both services", err)
	}
	if strings.Contains(err.Error(), cliBrokerKey) {
		t.Errorf("the refusal repeats the key: %v", err)
	}
	if len(gwKeys()) != 0 || len(brokerSeen()) != 0 {
		t.Errorf("requests were sent before the refusal: gateway %d, broker %d", len(gwKeys()), len(brokerSeen()))
	}
}

// TestRunCommandHasNoFlagThatTakesTheBrokerKeyValue: the key value on the command
// line would sit in the process list, so no such flag exists.
func TestRunCommandHasNoFlagThatTakesTheBrokerKeyValue(t *testing.T) {
	clearBrokerKeyEnv(t)
	gw, gwKeys := recordingGateway(t)
	broker, brokerSeen := keyedBroker(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)

	for _, flagName := range []string{"--mcp-broker-key", "--broker-key"} {
		err := runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, flagName, cliBrokerKey, dir})
		if err == nil || exitCode(err) != exitUsage {
			t.Fatalf("%s: err = %v, want a usage error so the key stays off argv", flagName, err)
		}
	}
	if len(gwKeys()) != 0 || len(brokerSeen()) != 0 {
		t.Errorf("requests were sent: gateway %d, broker %d", len(gwKeys()), len(brokerSeen()))
	}
}

// TestRunCommandBrokerKeyNeverReachesTheReportOrTheEvents: the broker echoes the
// key into a header and a body; neither the printed report, the saved JSON nor
// the event stream may carry it.
func TestRunCommandBrokerKeyNeverReachesTheReportOrTheEvents(t *testing.T) {
	clearBrokerKeyEnv(t)
	gw, _ := recordingGateway(t)
	// A broker that echoes the key in every reply, as a non-protocol reply.
	var broker *httptest.Server
	broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("ok"))
			return
		}
		k := r.Header.Get("x-fuse-key")
		w.Header().Set("x-echo", k)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("refused " + k))
	}))
	t.Cleanup(broker.Close)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)
	saved := filepath.Join(t.TempDir(), "report.json")
	events := filepath.Join(t.TempDir(), "events.ndjson")

	var err error
	out := captureStdout(t, func() {
		err = runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, "--mcp-broker-key-file", brokerKeyFile(t, cliBrokerKey),
			"--format", "json", "--save", saved, "--events", events, dir})
	})
	if exitCode(err) != exitFindings {
		t.Fatalf("exit = %d, want a finding so there is a report to inspect", exitCode(err))
	}
	onDisk, readErr := os.ReadFile(saved)
	if readErr != nil {
		t.Fatal(readErr)
	}
	stream, readErr := os.ReadFile(events)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(stream) == 0 {
		t.Fatal("no events were written, so this measured nothing")
	}
	for name, body := range map[string]string{"stdout": out, "saved report": string(onDisk), "event stream": string(stream)} {
		if strings.Contains(body, cliBrokerKey) {
			t.Errorf("the broker key appears in the %s:\n%s", name, body)
		}
	}
}
