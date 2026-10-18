package runner

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/mockryx/internal/scenario"
)

// testClientKey is a fixture, not a credential: it exists only in these tests.
const testClientKey = "mockryx-fixture-client-key-0001"

// seenRequest is what one request that reached a stub looked like.
type seenRequest struct {
	path, key, agent string
}

// recorder is a stub that serves the gateway's two routes and the broker, on one
// port, and records every request it receives. reply decides each answer.
type recorder struct {
	mu   sync.Mutex
	seen []seenRequest
}

func (r *recorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, seenRequest{
		path:  req.URL.Path,
		key:   req.Header.Get("x-fuse-key"),
		agent: req.Header.Get("x-fuse-agent-id"),
	})
}

func (r *recorder) requests() []seenRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seenRequest(nil), r.seen...)
}

// stubAll serves /v1/messages, /v1/fuse/check-tool-call and /broker. Each answer
// comes from the matching function, so one test can script every route.
func stubAll(t *testing.T, rec *recorder, messages, toolCheck, broker func(*http.Request) (int, map[string]string, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		rec.record(req)
		var fn func(*http.Request) (int, map[string]string, string)
		switch req.URL.Path {
		case "/v1/messages":
			fn = messages
		case checkToolCallPath:
			fn = toolCheck
		case "/broker":
			fn = broker
		default:
			http.NotFound(w, req)
			return
		}
		status, headers, body := fn(req)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func answerOK(*http.Request) (int, map[string]string, string) { return http.StatusOK, nil, `{}` }

func answerAllow(*http.Request) (int, map[string]string, string) {
	return http.StatusOK, nil, `{"decision":"allow","governed":true}`
}

func answerRPCResult(*http.Request) (int, map[string]string, string) {
	return http.StatusOK, nil, `{"jsonrpc":"2.0","id":1,"result":{}}`
}

// messagesStep is a plain messages step expecting status 200.
func messagesStep(name, agent string) scenario.Step {
	return scenario.Step{
		Name: name,
		Request: scenario.Request{
			Model:    "claude-haiku",
			Messages: []scenario.Message{{Role: "user", Content: "hi"}},
		},
		Headers: scenario.Headers{AgentID: agent},
		Expect:  scenario.Expect{Status: http.StatusOK},
	}
}

func toolCheckStep(name, agent string) scenario.Step {
	return scenario.Step{
		Name:      name,
		ToolCheck: &scenario.ToolCheck{Tool: "http_post"},
		Headers:   scenario.Headers{AgentID: agent},
		Expect:    scenario.Expect{Decision: "allow"},
	}
}

func mcpStep(name, agent string) scenario.Step {
	return scenario.Step{
		Name:    name,
		MCP:     &scenario.MCPCall{Method: "tools/call"},
		Headers: scenario.Headers{AgentID: agent},
		Expect:  scenario.Expect{Result: true},
	}
}

func TestMessagesStepSendsClientKeyAsXFuseKey(t *testing.T) {
	rec := &recorder{}
	srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)
	s := scenario.Scenario{Name: "keyed", Steps: []scenario.Step{messagesStep("m", "agent://test/bot")}}

	RunWithIdentity(s, srv.URL, "", "", Identity{ClientKey: testClientKey}, nil)

	got := rec.requests()
	if len(got) != 1 || got[0].key != testClientKey {
		t.Fatalf("gateway saw %+v, want one request carrying x-fuse-key", got)
	}
}

func TestToolCheckStepSendsClientKeyAsXFuseKey(t *testing.T) {
	rec := &recorder{}
	srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)
	s := scenario.Scenario{Name: "keyed", Requires: "taint", Steps: []scenario.Step{toolCheckStep("t", "agent://test/bot")}}

	RunWithIdentity(s, srv.URL, "", "", Identity{ClientKey: testClientKey}, nil)

	got := rec.requests()
	if len(got) != 1 || got[0].path != checkToolCallPath || got[0].key != testClientKey {
		t.Fatalf("gateway saw %+v, want one check-tool-call carrying x-fuse-key", got)
	}
}

func TestMCPStepNeverSendsClientKey(t *testing.T) {
	rec := &recorder{}
	srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)
	s := scenario.Scenario{Name: "broker", Requires: "mcp-dlp", Steps: []scenario.Step{mcpStep("b", "agent://test/bot")}}

	res := RunWithIdentity(s, srv.URL, srv.URL+"/broker", "", Identity{ClientKey: testClientKey}, nil)

	if res.Status != StatusPassed {
		t.Fatalf("status = %s, want passed so the broker was reached", res.Status)
	}
	for _, r := range rec.requests() {
		if r.path != "/broker" {
			t.Errorf("request to %s, want only the broker", r.path)
		}
		if r.key != "" {
			t.Errorf("the broker received x-fuse-key %q; the gateway key must never leave for the broker", r.key)
		}
	}
}

func TestNoClientKeyConfiguredSendsNoXFuseKeyHeader(t *testing.T) {
	rec := &recorder{}
	srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)
	s := scenario.Scenario{Name: "unkeyed", Steps: []scenario.Step{messagesStep("m", "agent://test/bot")}}

	RunWithIdentity(s, srv.URL, "", "", Identity{}, nil)

	for _, r := range rec.requests() {
		if r.key != "" {
			t.Errorf("x-fuse-key %q sent with no client key configured", r.key)
		}
	}
}

func TestAgentIDOverrideReplacesTheScenarioAgentIDOnEveryStepKind(t *testing.T) {
	rec := &recorder{}
	srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)
	s := scenario.Scenario{
		Name:     "three-kinds",
		Requires: "taint",
		Steps: []scenario.Step{
			messagesStep("m", "agent://test/scenario-own"),
			toolCheckStep("t", "agent://test/scenario-own"),
			mcpStep("b", "agent://test/scenario-own"),
		},
	}

	RunWithIdentity(s, srv.URL, srv.URL+"/broker", "", Identity{AgentID: "agent://td/agents/mockryx"}, nil)

	got := rec.requests()
	if len(got) != 3 {
		t.Fatalf("got %d requests, want 3", len(got))
	}
	for _, r := range got {
		if r.agent != "agent://td/agents/mockryx" {
			t.Errorf("%s sent x-fuse-agent-id %q, want the configured id", r.path, r.agent)
		}
	}
}

func TestAgentIDWithoutOverrideKeepsTheScenarioAgentID(t *testing.T) {
	rec := &recorder{}
	srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)
	s := scenario.Scenario{Name: "own", Steps: []scenario.Step{messagesStep("m", "agent://test/scenario-own")}}

	RunWithIdentity(s, srv.URL, "", "", Identity{}, nil)

	if got := rec.requests(); len(got) != 1 || got[0].agent != "agent://test/scenario-own" {
		t.Fatalf("gateway saw %+v, want the scenario's own agent id", got)
	}
}

func TestUnauthorizedGatewayReplyIsRefusedCredentialNotGuardrail(t *testing.T) {
	rec := &recorder{}
	refuse := func(*http.Request) (int, map[string]string, string) {
		return http.StatusUnauthorized, nil, `{"error":{"type":"unauthorized"}}`
	}
	srv := stubAll(t, rec, refuse, refuse, answerRPCResult)

	res := RunWithIdentity(wardryxScenario(), srv.URL, "", "", Identity{ClientKey: testClientKey}, nil)

	if res.Status == StatusSkipped {
		t.Fatalf("status = skipped_not_configured: a refused key must never read as a guardrail that is not configured")
	}
	if res.Status != StatusFailed || len(res.Findings) != 1 {
		t.Fatalf("status = %s with %d findings, want failed with one finding", res.Status, len(res.Findings))
	}
	if !strings.Contains(res.Findings[0].Detail, "the gateway refused the drill's credential") {
		t.Errorf("detail = %q, want it to say the gateway refused the drill's credential", res.Findings[0].Detail)
	}
}

func TestIdentityMismatchReplyIsRefusedCredentialNotGuardrail(t *testing.T) {
	rec := &recorder{}
	mismatch := func(*http.Request) (int, map[string]string, string) {
		return http.StatusForbidden, nil, `{"error":{"type":"identity_mismatch","message":"agent id is outside this key's binding"}}`
	}
	srv := stubAll(t, rec, mismatch, mismatch, answerRPCResult)

	res := RunWithIdentity(wardryxScenario(), srv.URL, "", "", Identity{ClientKey: testClientKey}, nil)

	if res.Status != StatusFailed || len(res.Findings) != 1 {
		t.Fatalf("status = %s with %d findings, want failed: a 403 identity_mismatch must not pass as the guardrail's deny", res.Status, len(res.Findings))
	}
	if !strings.Contains(res.Findings[0].Detail, "the gateway refused the drill's credential") {
		t.Errorf("detail = %q, want it to say the gateway refused the drill's credential", res.Findings[0].Detail)
	}
}

func TestRefusedCredentialStopsTheScenario(t *testing.T) {
	rec := &recorder{}
	refuse := func(*http.Request) (int, map[string]string, string) {
		return http.StatusUnauthorized, nil, `{}`
	}
	srv := stubAll(t, rec, refuse, refuse, answerRPCResult)
	s := scenario.Scenario{
		Name:     "two-steps",
		Requires: "wardryx",
		Steps:    []scenario.Step{messagesStep("first", "agent://test/bot"), messagesStep("second", "agent://test/bot")},
	}

	res := RunWithIdentity(s, srv.URL, "", "", Identity{}, nil)

	if n := len(rec.requests()); n != 1 {
		t.Errorf("gateway received %d requests, want 1: the second step cannot succeed once the credential is refused", n)
	}
	if len(res.Findings) != 1 {
		t.Errorf("findings = %d, want 1", len(res.Findings))
	}
}

func TestRefusalWithNoKeyNamesTheSettingToSet(t *testing.T) {
	rec := &recorder{}
	refuse := func(*http.Request) (int, map[string]string, string) { return http.StatusUnauthorized, nil, `{}` }
	srv := stubAll(t, rec, refuse, refuse, answerRPCResult)
	s := scenario.Scenario{Name: "no-key", Steps: []scenario.Step{messagesStep("m", "agent://test/bot")}}

	res := RunWithIdentity(s, srv.URL, "", "", Identity{}, nil)

	if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Detail, "--client-key-file") {
		t.Fatalf("findings = %+v, want one whose detail names --client-key-file", res.Findings)
	}
}

func TestRefusalWithAKeyDoesNotAskForOne(t *testing.T) {
	rec := &recorder{}
	refuse := func(*http.Request) (int, map[string]string, string) { return http.StatusUnauthorized, nil, `{}` }
	srv := stubAll(t, rec, refuse, refuse, answerRPCResult)
	s := scenario.Scenario{Name: "keyed", Steps: []scenario.Step{messagesStep("m", "agent://test/bot")}}

	res := RunWithIdentity(s, srv.URL, "", "", Identity{ClientKey: testClientKey}, nil)

	if len(res.Findings) != 1 || strings.Contains(res.Findings[0].Detail, "--client-key-file") {
		t.Fatalf("findings = %+v, want the refusal without the request for a key, since one was sent", res.Findings)
	}
}

func TestPlainForbiddenWithNoSignalStaysASkip(t *testing.T) {
	rec := &recorder{}
	plain := func(*http.Request) (int, map[string]string, string) {
		return http.StatusForbidden, nil, `{"error":"denied"}`
	}
	srv := stubAll(t, rec, plain, plain, answerRPCResult)

	res := RunWithIdentity(wardryxScenario(), srv.URL, "", "", Identity{ClientKey: testClientKey}, nil)

	if res.Status != StatusSkipped {
		t.Fatalf("status = %s, want skipped_not_configured: a 403 that names no credential is still a guardrail answer", res.Status)
	}
}

func TestFindingsNeverCarryTheClientKey(t *testing.T) {
	rec := &recorder{}
	echo := func(*http.Request) (int, map[string]string, string) {
		return http.StatusOK, map[string]string{"x-echo": testClientKey}, "not json " + testClientKey
	}
	srv := stubAll(t, rec, echo, echo, answerRPCResult)
	s := scenario.Scenario{
		Name:  "echo",
		Steps: []scenario.Step{messagesStep("m", "agent://test/bot"), toolCheckStep("t", "agent://test/bot")},
	}
	s.Requires = "taint"

	res := RunWithIdentity(s, srv.URL, "", "", Identity{ClientKey: testClientKey}, nil)

	out, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) == 0 {
		t.Fatal("the scenario produced no findings, so this measured nothing")
	}
	if strings.Contains(string(out), testClientKey) {
		t.Errorf("the serialised result contains the client key: %s", out)
	}
}
