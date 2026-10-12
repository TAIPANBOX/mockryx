package runner

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/mockryx/internal/scenario"
)

// Tests for the two step kinds added on 2026-10-12, tool_check and mcp, and
// for tool history on a messages step. Each was run red first; see
// VALIDATION.md.

func mustParse(t *testing.T, y string) scenario.Scenario {
	t.Helper()
	s, err := scenario.ParseYAML([]byte(y))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	return s
}

// seen is what a stub recorded about one request.
type seen struct {
	path    string
	headers http.Header
	body    string
}

// fakeFirewall is a gateway stand-in: /v1/messages answers 200 and records the
// request, /v1/fuse/check-tool-call answers whatever answer() says.
type fakeFirewall struct {
	t        *testing.T
	mu       sync.Mutex
	requests []seen
	answer   func(runID, tool string) (status int, body string)
	srv      *httptest.Server
}

func newFakeFirewall(t *testing.T, answer func(runID, tool string) (int, string)) *fakeFirewall {
	t.Helper()
	f := &fakeFirewall{t: t, answer: answer}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, seen{r.URL.Path, r.Header.Clone(), string(raw)})
		f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/messages":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case "/v1/fuse/check-tool-call":
			var in struct {
				RunID string `json:"run_id"`
				Tool  string `json:"tool"`
			}
			_ = json.Unmarshal(raw, &in)
			status, body := f.answer(in.RunID, in.Tool)
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFirewall) last(path string) seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if f.requests[i].path == path {
			return f.requests[i]
		}
	}
	f.t.Fatalf("no request to %s was recorded", path)
	return seen{}
}

const (
	decisionDeny            = `{"decision":"deny","governed":true,"rule":"no-exec-after-untrusted","denied":["network_egress"]}`
	decisionAllowGoverned   = `{"decision":"allow","governed":true}`
	decisionAllowUngoverned = `{"decision":"allow","governed":false,"reason":"the agent firewall is off on this gateway; nothing judged this call"}`
)

// egressScenario is the shape the shipped drill has: read the web into the
// run, then ask whether http_post may run.
const egressScenario = `
name: egress-after-untrusted-read
requires: taint
steps:
  - name: read-the-web
    request:
      model: claude-haiku
      messages:
        - role: user
          content: "Summarise the page."
        - role: assistant
          content:
            - type: tool_use
              id: toolu_01
              name: fetch_url
        - role: user
          content:
            - type: tool_result
              tool_use_id: toolu_01
              content: "page text"
      tools:
        - name: fetch_url
    headers:
      agent_id: agent://mockryx.local/rehearsal/researcher
    expect:
      status: 200
  - name: egress-is-refused
    tool_check:
      tool: http_post
    headers:
      agent_id: agent://mockryx.local/rehearsal/researcher
    expect:
      decision: deny
`

func TestToolCheckStepPassesWhenTheDecisionMatches(t *testing.T) {
	f := newFakeFirewall(t, func(string, string) (int, string) { return 200, decisionDeny })
	res := Run(mustParse(t, egressScenario), f.srv.URL, "", nil)
	if res.Status != StatusPassed || len(res.Findings) != 0 {
		t.Fatalf("Status = %q, findings = %+v, want passed", res.Status, res.Findings)
	}
	if res.Metrics.Calls != 2 {
		t.Errorf("Calls = %d, want 2 (one messages step, one tool_check)", res.Metrics.Calls)
	}
}

func TestToolCheckStepAsksAboutTheScenariosOwnRun(t *testing.T) {
	f := newFakeFirewall(t, func(string, string) (int, string) { return 200, decisionDeny })
	_ = Run(mustParse(t, egressScenario), f.srv.URL, "key-for-gateway", nil)

	msg := f.last("/v1/messages")
	chk := f.last("/v1/fuse/check-tool-call")

	wireRun := msg.headers.Get("x-fuse-run-id")
	if wireRun == "" {
		t.Fatal("the messages step sent no x-fuse-run-id")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(chk.body), &body); err != nil {
		t.Fatalf("check-tool-call body is not JSON: %v: %s", err, chk.body)
	}
	if body["run_id"] != wireRun {
		t.Errorf("check-tool-call asked about run %v, but the history went in under %q: taint is per run, so these must be the same run", body["run_id"], wireRun)
	}
	if body["tool"] != "http_post" {
		t.Errorf("tool = %v, want http_post", body["tool"])
	}
	if got := chk.headers.Get("x-fuse-agent-id"); got != "agent://mockryx.local/rehearsal/researcher" {
		t.Errorf("x-fuse-agent-id = %q: the gateway files its taint_block event under this header", got)
	}
	if got := chk.headers.Get("x-api-key"); got != "key-for-gateway" {
		t.Errorf("x-api-key = %q, want the gateway key", got)
	}
	if got := chk.headers.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
}

func TestToolHistoryReachesTheGatewayInTheAnthropicWireShape(t *testing.T) {
	f := newFakeFirewall(t, func(string, string) (int, string) { return 200, decisionDeny })
	_ = Run(mustParse(t, egressScenario), f.srv.URL, "", nil)

	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(f.last("/v1/messages").body), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("got %d messages", len(body.Messages))
	}
	if s, ok := body.Messages[0].Content.(string); !ok || s != "Summarise the page." {
		t.Errorf("plain content = %#v, want the string", body.Messages[0].Content)
	}
	blocks, ok := body.Messages[1].Content.([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("assistant content = %#v, want a list of one block", body.Messages[1].Content)
	}
	use := blocks[0].(map[string]any)
	if use["type"] != "tool_use" || use["name"] != "fetch_url" || use["id"] != "toolu_01" {
		t.Errorf("tool_use block = %v", use)
	}
	if _, has := use["input"]; !has {
		t.Errorf("tool_use block has no input key, which the wire requires: %v", use)
	}
}

func TestToolCheckStepFindingWhenTheFirewallEnforcesNothing(t *testing.T) {
	// governed:true says the firewall judged the call; a decision of allow
	// where deny was expected is the gap.
	f := newFakeFirewall(t, func(string, string) (int, string) { return 200, decisionAllowGoverned })
	res := Run(mustParse(t, egressScenario), f.srv.URL, "", nil)
	if res.Status != StatusFailed || len(res.Findings) != 1 {
		t.Fatalf("Status = %q, findings = %+v, want failed with one finding", res.Status, res.Findings)
	}
	fd := res.Findings[0]
	if fd.Step != "egress-is-refused" || fd.Kind != "tool_check" {
		t.Errorf("finding = %+v", fd)
	}
	if !strings.Contains(fd.Expect, "deny") || !strings.Contains(fd.Got, "allow") {
		t.Errorf("Expect = %q, Got = %q: the report must say what was wanted and what came back", fd.Expect, fd.Got)
	}
	if fd.ExpectStatus != 0 {
		t.Errorf("ExpectStatus = %d, a tool_check has no status to expect", fd.ExpectStatus)
	}
}

func TestToolCheckAnsweringUngovernedReadsAsNotConfigured(t *testing.T) {
	// The off firewall's answer. Not a gap, and not a pass either.
	f := newFakeFirewall(t, func(string, string) (int, string) { return 200, decisionAllowUngoverned })
	res := Run(mustParse(t, egressScenario), f.srv.URL, "", nil)
	if res.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped_not_configured: governed:false is the firewall saying it is off", res.Status)
	}
	if len(res.Findings) != 0 || len(res.SkippedFindings) != 1 {
		t.Fatalf("findings = %+v, skipped = %+v", res.Findings, res.SkippedFindings)
	}
	// Skipped for the right reason: the deny step's own mismatch, read from
	// the decision, kept where a human can still see it.
	sk := res.SkippedFindings[0]
	if sk.Kind != "tool_check" || sk.Step != "egress-is-refused" || !strings.Contains(sk.Got, "allow") {
		t.Errorf("skipped finding = %+v", sk)
	}
}

func TestToolCheckGovernedInAnyStepIsTheSignal(t *testing.T) {
	// A control step that is allowed AND governed proves the firewall is on;
	// the later deny that never came is then a gap, not a skip. This is the
	// shipped drill's before-and-after shape.
	y := strings.Replace(egressScenario, "steps:\n", `steps:
  - name: a-clean-run-may-post
    tool_check: {tool: http_post}
    expect: {decision: allow}
`, 1)
	f := newFakeFirewall(t, func(string, string) (int, string) { return 200, decisionAllowGoverned })
	res := Run(mustParse(t, y), f.srv.URL, "", nil)
	if res.Status != StatusFailed || len(res.Findings) != 1 || res.Findings[0].Step != "egress-is-refused" {
		t.Fatalf("Status = %q, findings = %+v, want failed on the deny step only", res.Status, res.Findings)
	}
}

func TestToolCheckShadowModeIsAGapNotASkip(t *testing.T) {
	// Shadow judges (governed:true) and allows, handing back what it WOULD
	// have refused. A firewall that only observes does not hold the line.
	f := newFakeFirewall(t, func(string, string) (int, string) {
		return 200, `{"decision":"allow","governed":true,"would_block":"tainted context [web] denies capability [network_egress]"}`
	})
	res := Run(mustParse(t, egressScenario), f.srv.URL, "", nil)
	if res.Status != StatusFailed {
		t.Fatalf("Status = %q, want failed", res.Status)
	}
}

func TestToolCheckAnAnswerThatIsNotADecisionIsAHardFindingNotASkip(t *testing.T) {
	cases := map[string]func(string, string) (int, string){
		"404, a gateway without the endpoint": func(string, string) (int, string) { return 404, `not found` },
		"200 but not JSON":                    func(string, string) (int, string) { return 200, `<html>proxy</html>` },
		"200 JSON with no decision":           func(string, string) (int, string) { return 200, `{"governed":false}` },
		"500":                                 func(string, string) (int, string) { return 500, `{"decision":"deny","governed":true}` },
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeFirewall(t, answer)
			res := Run(mustParse(t, egressScenario), f.srv.URL, "", nil)
			if res.Status != StatusFailed {
				t.Fatalf("Status = %q, want failed: an unreadable answer is never evidence the firewall is merely off", res.Status)
			}
			if len(res.Findings) != 1 || res.Findings[0].Kind != "tool_check" ||
				!strings.Contains(res.Findings[0].Detail, "check-tool-call") {
				t.Errorf("findings = %+v, want one tool_check finding naming check-tool-call", res.Findings)
			}
		})
	}
}

func TestToolCheckEventIsWatchedOnlyAfterTheDecisionMatches(t *testing.T) {
	y := egressScenario + "      event: {source: tokenfuse, type: taint_block}\n"

	f := newFakeFirewall(t, func(string, string) (int, string) { return 200, decisionDeny })
	w := &fakeWatcher{ok: true, event: event.Event{Source: "tokenfuse", Type: "taint_block"}}
	res := Run(mustParse(t, y), f.srv.URL, "", w)
	if res.Status != StatusPassed {
		t.Fatalf("Status = %q, findings = %+v", res.Status, res.Findings)
	}
	wire := f.last("/v1/messages").headers.Get("x-fuse-run-id")
	if w.calledRunID != wire || w.calledSource != "tokenfuse" || w.calledType != "taint_block" {
		t.Errorf("watched run=%q source=%q type=%q, want run %q tokenfuse/taint_block", w.calledRunID, w.calledSource, w.calledType, wire)
	}

	// The decision did not match, so there is no reaction to wait for.
	f2 := newFakeFirewall(t, func(string, string) (int, string) { return 200, decisionAllowGoverned })
	w2 := &fakeWatcher{ok: true}
	res2 := Run(mustParse(t, y), f2.srv.URL, "", w2)
	if res2.Status != StatusFailed {
		t.Fatalf("Status = %q, want failed", res2.Status)
	}
	if w2.calledRunID != "" {
		t.Errorf("the watcher was consulted (%q) after a mismatched decision", w2.calledRunID)
	}
}

// --- mcp ---------------------------------------------------------------

// fakeBroker answers the JSON-RPC requests it receives with whatever reply()
// returns and records them.
type fakeBroker struct {
	mu       sync.Mutex
	requests []seen
	srv      *httptest.Server
}

func newFakeBroker(t *testing.T, reply func(req map[string]any) (status int, body string)) *fakeBroker {
	t.Helper()
	b := &fakeBroker{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.requests = append(b.requests, seen{r.URL.Path, r.Header.Clone(), string(raw)})
		b.mu.Unlock()
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		status, body := reply(req)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func rpcError(code int, msg string) func(map[string]any) (int, string) {
	return func(req map[string]any) (int, string) {
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req["id"], "error": map[string]any{"code": code, "message": msg}})
		return 200, string(out)
	}
}

func rpcResult(req map[string]any) (int, string) {
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"content": []any{}}})
	return 200, string(out)
}

const mcpSecretScenario = `
name: mcp-secret-in-arguments
requires: mcp-dlp
steps:
  - name: raw-secret-in-arguments
    mcp:
      method: tools/call
      params:
        name: search_notes
        arguments:
          query: "AKIAIOSFODNN7EXAMPLE"
    headers:
      agent_id: agent://mockryx.local/rehearsal/notetaker
    expect:
      error_code: -32002
      error_contains: blocked
`

func TestMCPStepSendsOneJSONRPCRequestToTheBroker(t *testing.T) {
	b := newFakeBroker(t, rpcError(-32002, "blocked: raw secret in tool arguments (aws_access_key)"))
	_ = RunWithBroker(mustParse(t, mcpSecretScenario), "http://127.0.0.1:1", b.srv.URL, "gateway-key", nil)

	if len(b.requests) != 1 {
		t.Fatalf("the broker received %d requests, want exactly 1", len(b.requests))
	}
	r := b.requests[0]
	var req struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      any            `json:"id"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(r.body), &req); err != nil {
		t.Fatalf("not JSON: %v: %s", err, r.body)
	}
	if req.JSONRPC != "2.0" || req.ID == nil || req.Method != "tools/call" || req.Params["name"] != "search_notes" {
		t.Errorf("request = %+v", req)
	}
	args, _ := req.Params["arguments"].(map[string]any)
	if args["query"] != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("params.arguments = %v", req.Params["arguments"])
	}
	if got := r.headers.Get("x-fuse-agent-id"); got != "agent://mockryx.local/rehearsal/notetaker" {
		t.Errorf("x-fuse-agent-id = %q", got)
	}
	if got := r.headers.Get("x-fuse-run-id"); !strings.HasPrefix(got, "mockryx-mcp-secret-in-arguments-") {
		t.Errorf("x-fuse-run-id = %q, want the scenario's minted run id", got)
	}
	if got := r.headers.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	// The gateway key belongs to the gateway; it must not travel to the broker.
	if got := r.headers.Get("x-api-key"); got != "" {
		t.Errorf("the gateway's x-api-key was sent to the broker: %q", got)
	}
}

func TestMCPStepSendsNothingToTheGateway(t *testing.T) {
	gw := newFakeFirewall(t, func(string, string) (int, string) { return 200, decisionDeny })
	b := newFakeBroker(t, rpcError(-32002, "blocked: raw secret in tool arguments"))
	_ = RunWithBroker(mustParse(t, mcpSecretScenario), gw.srv.URL, b.srv.URL, "", nil)
	if len(gw.requests) != 0 {
		t.Errorf("the gateway received %d requests from an mcp-only scenario, want 0", len(gw.requests))
	}
}

func TestMCPStepPassesOnTheExpectedErrorCode(t *testing.T) {
	b := newFakeBroker(t, rpcError(-32002, "blocked: raw secret in tool arguments (aws_access_key)"))
	res := RunWithBroker(mustParse(t, mcpSecretScenario), "http://127.0.0.1:1", b.srv.URL, "", nil)
	if res.Status != StatusPassed || len(res.Findings) != 0 {
		t.Fatalf("Status = %q, findings = %+v, want passed", res.Status, res.Findings)
	}
}

func TestMCPStepFindingWhenAnotherGuardrailSpeaksInstead(t *testing.T) {
	// The broker is plainly governing (a guardrail-range code), but the raw
	// secret was not stopped by the secret scan: a gap, not a skip.
	b := newFakeBroker(t, rpcError(-32004, "blocked: policy denied tool \"search_notes\""))
	res := RunWithBroker(mustParse(t, mcpSecretScenario), "http://127.0.0.1:1", b.srv.URL, "", nil)
	if res.Status != StatusFailed || len(res.Findings) != 1 {
		t.Fatalf("Status = %q, findings = %+v, want failed", res.Status, res.Findings)
	}
	fd := res.Findings[0]
	if fd.Kind != "mcp" || !strings.Contains(fd.Expect, "-32002") || !strings.Contains(fd.Got, "-32004") {
		t.Errorf("finding = %+v", fd)
	}
}

func TestMCPAResultWithNoGuardrailSpeakingReadsAsNotConfigured(t *testing.T) {
	// The broker with DLP off forwards the call and returns the upstream's
	// result. Nothing on the wire says whether the scan is off or broken, so
	// by the same convention as every optional guardrail it is "not
	// configured", and --fail-on-skip is the operator's way to insist.
	b := newFakeBroker(t, rpcResult)
	res := RunWithBroker(mustParse(t, mcpSecretScenario), "http://127.0.0.1:1", b.srv.URL, "", nil)
	if res.Status != StatusSkipped || len(res.SkippedFindings) != 1 || len(res.Findings) != 0 {
		t.Fatalf("Status = %q, findings = %+v, skipped = %+v, want skipped_not_configured", res.Status, res.Findings, res.SkippedFindings)
	}
}

func TestMCPAnUpstreamsOwnErrorIsNotAGuardrailSignal(t *testing.T) {
	// -32602 is JSON-RPC's invalid params: an upstream's error, outside the
	// range the broker reserves for its guardrails (-32001 to -32010).
	b := newFakeBroker(t, rpcError(-32602, "unknown tool"))
	res := RunWithBroker(mustParse(t, mcpSecretScenario), "http://127.0.0.1:1", b.srv.URL, "", nil)
	if res.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped_not_configured", res.Status)
	}
}

func TestMCPErrorContainsRefusesAnUpstreamsErrorWithTheSameNumber(t *testing.T) {
	// The MCP spec gives -32002 a meaning of its own (resource not found), so
	// an upstream can raise the very number the broker's secret block uses.
	// With error_contains the drill does not pass on an error the broker
	// never raised.
	b := newFakeBroker(t, rpcError(-32002, "resource not found"))
	res := RunWithBroker(mustParse(t, mcpSecretScenario), "http://127.0.0.1:1", b.srv.URL, "", nil)
	if res.Status != StatusFailed || len(res.Findings) != 1 || res.Findings[0].Kind != "mcp" ||
		!strings.Contains(res.Findings[0].Got, "resource not found") {
		t.Fatalf("Status = %q, findings = %+v: want a gap naming the error that came back; the secret scan never ran", res.Status, res.Findings)
	}
}

func TestMCPStepCanExpectAResult(t *testing.T) {
	y := strings.Replace(mcpSecretScenario, "      error_code: -32002\n      error_contains: blocked\n", "      result: true\n", 1)

	pass := RunWithBroker(mustParse(t, y), "http://127.0.0.1:1", newFakeBroker(t, rpcResult).srv.URL, "", nil)
	if pass.Status != StatusPassed {
		t.Errorf("a result where a result was expected: Status = %q, findings = %+v", pass.Status, pass.Findings)
	}
	fail := RunWithBroker(mustParse(t, y), "http://127.0.0.1:1", newFakeBroker(t, rpcError(-32004, "blocked: policy")).srv.URL, "", nil)
	if fail.Status != StatusFailed {
		t.Errorf("an error where a result was expected: Status = %q, want failed", fail.Status)
	}
}

func TestMCPStepWithoutABrokerIsAHardFinding(t *testing.T) {
	res := RunWithBroker(mustParse(t, mcpSecretScenario), "http://127.0.0.1:1", "", "", nil)
	if res.Status != StatusFailed || len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Detail, "--mcp-broker") {
		t.Fatalf("Status = %q, findings = %+v, want a hard finding naming --mcp-broker", res.Status, res.Findings)
	}
}

func TestMCPAnUnreadableReplyIsAHardFindingNotASkip(t *testing.T) {
	cases := map[string]func(map[string]any) (int, string){
		"401, a broker whose door wants a credential": func(map[string]any) (int, string) { return 401, `unauthorized` },
		"200 but not JSON-RPC":                        func(map[string]any) (int, string) { return 200, `{"hello":"world"}` },
		"200 and not JSON":                            func(map[string]any) (int, string) { return 200, `<html>` },
		// The broker's door refusal, byte for byte what a v1.10.0 broker with
		// TOKENFUSE_MCP_KEYS set answers a caller that sent no key: an "error"
		// object, but not a JSON-RPC one (no code). Found by firing the drills
		// at that broker: it was read as an error with code 0 and the drills
		// reported skipped_not_configured for a broker that had refused to
		// listen, which is exactly the false reading this case exists to stop.
		"401, the door refusal, an error object with no code": func(map[string]any) (int, string) {
			return 401, `{"error":{"type":"unauthorized","reason":"this gateway requires a client credential in the ` + "`x-fuse-key`" + ` header","retryable":false}}`
		},
		"200, an error with a string code": func(map[string]any) (int, string) {
			return 200, `{"jsonrpc":"2.0","id":1,"error":{"code":"-32002","message":"blocked"}}`
		},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			b := newFakeBroker(t, reply)
			res := RunWithBroker(mustParse(t, mcpSecretScenario), "http://127.0.0.1:1", b.srv.URL, "", nil)
			if res.Status != StatusFailed || len(res.Findings) != 1 || res.Findings[0].Kind != "mcp" ||
				!strings.Contains(res.Findings[0].Detail, "broker") {
				t.Fatalf("Status = %q, findings = %+v: an unreadable reply is a hard finding naming the broker, not evidence the guardrail is off", res.Status, res.Findings)
			}
		})
	}
}

func TestMCPTransportErrorIsAHardFinding(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // nothing listens here now
	res := RunWithBroker(mustParse(t, mcpSecretScenario), "http://127.0.0.1:1", "http://"+addr, "", nil)
	if res.Status != StatusFailed || len(res.Findings) != 1 || res.Findings[0].Kind != "mcp" {
		t.Fatalf("Status = %q, findings = %+v: unreachable is a hard finding, never read as not configured", res.Status, res.Findings)
	}
}

func TestProbeBrokerAnyHTTPAnswerMeansReachable(t *testing.T) {
	// A proxy in front of the broker may answer /healthz with a 404 and still
	// be the way in. Only a failure to connect is "unreachable".
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if err := ProbeBroker(srv.URL + "/mcp"); err != nil {
		t.Errorf("ProbeBroker on a live server answering 404: %v", err)
	}
}

func TestProbeBrokerRefusedConnectionIsUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	if err := ProbeBroker("http://" + addr); err == nil {
		t.Error("ProbeBroker returned nil for a port nothing listens on")
	}
	for _, bad := range []string{"", "not a url", "ftp://127.0.0.1:1"} {
		if err := ProbeBroker(bad); err == nil {
			t.Errorf("ProbeBroker(%q) = nil, want an error", bad)
		}
	}
}

func TestRunStillHasItsOriginalSignature(t *testing.T) {
	// Run keeps its four arguments, and a scenario with no mcp step needs no
	// broker. Compile-time as much as run-time.
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) { return 402, nil })
	if res := Run(runawayScenario(1, 1), srv.URL, "", nil); res.Status != StatusPassed {
		t.Errorf("Status = %q", res.Status)
	}
}
