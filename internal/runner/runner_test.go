package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/mockryx/internal/scenario"
)

// newStubGateway stands up a fake gateway for these tests: an
// httptest.Server whose POST /v1/messages handler decides its response by
// calling handle with a 1-indexed call counter, the received request, and
// its decoded JSON body. Mirrors the real gateway's canned failure modes
// (402 after N calls, 403 + x-fuse-wardryx: deny, 403 for DLP, 200
// otherwise) without ever needing a live TokenFuse gateway.
func newStubGateway(t *testing.T, handle func(call int, r *http.Request, body map[string]any) (status int, headers map[string]string)) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		calls++
		call := calls
		mu.Unlock()

		status, headers := handle(call, r, body)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runawayScenario(repeat, withinRepeats int) scenario.Scenario {
	return scenario.Scenario{
		Name: "runaway-budget",
		Steps: []scenario.Step{{
			Name:   "loop",
			Repeat: repeat,
			Request: scenario.Request{
				Model:    "claude-haiku",
				Messages: []scenario.Message{{Role: "user", Content: "keep going"}},
			},
			Headers: scenario.Headers{BudgetUSD: "0.001"},
			Expect:  scenario.Expect{Status: http.StatusPaymentRequired, WithinRepeats: withinRepeats},
		}},
	}
}

func TestRunPassesWhenBreakerTripsWithinRepeats(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		if call < 3 {
			return http.StatusOK, nil
		}
		return http.StatusPaymentRequired, nil
	})

	res := Run(runawayScenario(5, 5), srv.URL, "")
	if res.Status != StatusPassed {
		t.Errorf("Status = %q, want passed; findings = %+v", res.Status, res.Findings)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", res.Findings)
	}
	if res.Metrics.Calls != 3 {
		t.Errorf("Calls = %d, want 3 (stops at the first matching attempt)", res.Metrics.Calls)
	}
}

func TestRunFindingWhenBreakerNeverTrips(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusOK, nil
	})

	res := Run(runawayScenario(4, 4), srv.URL, "")
	if res.Status != StatusFailed {
		t.Fatalf("Status = %q, want failed", res.Status)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want 1", res.Findings)
	}
	if res.Metrics.Calls != 4 {
		t.Errorf("Calls = %d, want 4 (never matches, exhausts every repeat)", res.Metrics.Calls)
	}
	if res.Findings[0].GotStatus != http.StatusOK {
		t.Errorf("Finding.GotStatus = %d, want 200", res.Findings[0].GotStatus)
	}
	if res.Findings[0].ExpectStatus != http.StatusPaymentRequired {
		t.Errorf("Finding.ExpectStatus = %d, want 402", res.Findings[0].ExpectStatus)
	}
}

func TestRunFindingWhenBreakerFiresTooLate(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		if call < 5 {
			return http.StatusOK, nil
		}
		return http.StatusPaymentRequired, nil
	})

	res := Run(runawayScenario(5, 3), srv.URL, "")
	if res.Status != StatusFailed {
		t.Fatalf("Status = %q, want failed (fired on attempt 5, after within_repeats=3)", res.Status)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want 1", res.Findings)
	}
	if !strings.Contains(res.Findings[0].Detail, "within_repeats=3") {
		t.Errorf("Finding.Detail = %q, want it to mention the missed deadline", res.Findings[0].Detail)
	}
	if res.Metrics.Calls != 5 {
		t.Errorf("Calls = %d, want 5", res.Metrics.Calls)
	}
}

func wardryxScenario() scenario.Scenario {
	return scenario.Scenario{
		Name:     "wardryx-denied-tool",
		Requires: "wardryx",
		Steps: []scenario.Step{{
			Name: "request-shell-exec",
			Request: scenario.Request{
				Model:    "claude-haiku",
				Messages: []scenario.Message{{Role: "user", Content: "run a command"}},
				Tools:    []scenario.Tool{{Name: "shell_exec"}},
			},
			Expect: scenario.Expect{
				Status: http.StatusForbidden,
				Header: map[string]string{"x-fuse-wardryx": "deny"},
			},
		}},
	}
}

func TestRunHeaderMatcherPasses(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusForbidden, map[string]string{"x-fuse-wardryx": "deny"}
	})

	res := Run(wardryxScenario(), srv.URL, "")
	if res.Status != StatusPassed {
		t.Errorf("Status = %q, want passed; findings = %+v", res.Status, res.Findings)
	}
}

func TestRunHeaderMatcherFindingWhenGuardrailAllowsAnyway(t *testing.T) {
	// Wardryx is clearly wired in: it stamps x-fuse-wardryx on this call,
	// but decided to allow something that should have been denied. That is
	// a genuine gap, not a missing feature.
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusOK, map[string]string{"x-fuse-wardryx": "allow"}
	})

	res := Run(wardryxScenario(), srv.URL, "")
	if res.Status != StatusFailed {
		t.Fatalf("Status = %q, want failed", res.Status)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want 1", res.Findings)
	}
}

func TestRunSkippedWhenGuardrailNotConfigured(t *testing.T) {
	// This gateway never once mentions wardryx: it simply does not have
	// the feature wired in, so the scenario must be skipped, not failed.
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusOK, nil
	})

	res := Run(wardryxScenario(), srv.URL, "")
	if res.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped_not_configured", res.Status)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none: a missing feature is not a defensive gap", res.Findings)
	}
}

func TestRunSkippedKeepsDiscardedFindingOnSkippedFindings(t *testing.T) {
	// Same broken-or-absent shape as TestRunSkippedWhenGuardrailNotConfigured
	// above: the response mismatches Expect, but the signal header is never
	// seen, so this is Skipped, not Failed. Findings must stay empty (a
	// Skipped result is not a gap by default), but the raw mismatch must not
	// be thrown away outright: it belongs on SkippedFindings, for a human
	// reading the report, or an operator running --fail-on-skip, to still
	// see it.
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusOK, nil
	})

	res := Run(wardryxScenario(), srv.URL, "")
	if res.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped_not_configured", res.Status)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", res.Findings)
	}
	if len(res.SkippedFindings) != 1 {
		t.Fatalf("SkippedFindings = %+v, want 1", res.SkippedFindings)
	}
	if res.SkippedFindings[0].GotStatus != http.StatusOK {
		t.Errorf("SkippedFindings[0].GotStatus = %d, want 200", res.SkippedFindings[0].GotStatus)
	}
	if res.SkippedFindings[0].ExpectStatus != http.StatusForbidden {
		t.Errorf("SkippedFindings[0].ExpectStatus = %d, want 403", res.SkippedFindings[0].ExpectStatus)
	}
}

func TestRunTransportErrorIsAlwaysAFailure(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusOK, nil
	})
	badURL := srv.URL
	srv.Close() // now unreachable, even though this scenario declares Requires

	res := Run(wardryxScenario(), badURL, "")
	if res.Status != StatusFailed {
		t.Fatalf("Status = %q, want failed: a transport error must never read as skipped_not_configured", res.Status)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want 1", res.Findings)
	}
}

func TestRunHeaderMatchersAreCaseInsensitive(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusForbidden, map[string]string{"X-Fuse-Wardryx": "deny"}
	})

	res := Run(wardryxScenario(), srv.URL, "")
	if res.Status != StatusPassed {
		t.Errorf("Status = %q, want passed: header matching must be case-insensitive", res.Status)
	}
}

func TestRunSendsRunIDConsistentlyAcrossRepeats(t *testing.T) {
	var seen []string
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		seen = append(seen, r.Header.Get("x-fuse-run-id"))
		return http.StatusOK, nil
	})

	Run(runawayScenario(3, 3), srv.URL, "")
	if len(seen) != 3 {
		t.Fatalf("got %d calls, want 3", len(seen))
	}
	for i, id := range seen {
		if id == "" {
			t.Errorf("call %d: x-fuse-run-id was empty", i)
		}
		if id != seen[0] {
			t.Errorf("call %d: x-fuse-run-id = %q, want %q (must stay constant across repeats)", i, id, seen[0])
		}
	}
}

func TestRunSendsExplicitRunIDVerbatim(t *testing.T) {
	var got string
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		got = r.Header.Get("x-fuse-run-id")
		return http.StatusPaymentRequired, nil
	})

	s := runawayScenario(1, 1)
	s.Steps[0].Headers.RunID = "my-explicit-run-id"
	Run(s, srv.URL, "")
	if got != "my-explicit-run-id" {
		t.Errorf("x-fuse-run-id = %q, want my-explicit-run-id", got)
	}
}

func TestRunSendsAPIKeyAndHeaders(t *testing.T) {
	var gotAPIKey, gotAgentID, gotOnBehalfOf, gotTaskType, gotOutcome, gotApprovalToken string
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotAgentID = r.Header.Get("x-fuse-agent-id")
		gotOnBehalfOf = r.Header.Get("x-fuse-on-behalf-of")
		gotTaskType = r.Header.Get("x-fuse-task-type")
		gotOutcome = r.Header.Get("x-fuse-outcome")
		gotApprovalToken = r.Header.Get("x-fuse-approval-token")
		return http.StatusOK, nil
	})

	s := scenario.Scenario{
		Name: "headers-test",
		Steps: []scenario.Step{{
			Name: "step",
			Request: scenario.Request{
				Model:    "claude-haiku",
				Messages: []scenario.Message{{Role: "user", Content: "hi"}},
			},
			Headers: scenario.Headers{
				AgentID:       "agent://test/bot",
				OnBehalfOf:    "user://test/operator",
				TaskType:      "support_chat",
				Outcome:       "case_resolved",
				ApprovalToken: "approval-xyz",
			},
			Expect: scenario.Expect{Status: http.StatusOK},
		}},
	}
	Run(s, srv.URL, "test-api-key")

	if gotAPIKey != "test-api-key" {
		t.Errorf("x-api-key = %q", gotAPIKey)
	}
	if gotAgentID != "agent://test/bot" {
		t.Errorf("x-fuse-agent-id = %q", gotAgentID)
	}
	if gotOnBehalfOf != "user://test/operator" {
		t.Errorf("x-fuse-on-behalf-of = %q", gotOnBehalfOf)
	}
	if gotTaskType != "support_chat" {
		t.Errorf("x-fuse-task-type = %q", gotTaskType)
	}
	if gotOutcome != "case_resolved" {
		t.Errorf("x-fuse-outcome = %q", gotOutcome)
	}
	if gotApprovalToken != "approval-xyz" {
		t.Errorf("x-fuse-approval-token = %q", gotApprovalToken)
	}
}

func TestRunSendsRequestBodyShape(t *testing.T) {
	var gotBody map[string]any
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		gotBody = body
		return http.StatusOK, nil
	})

	s := scenario.Scenario{
		Name: "body-test",
		Steps: []scenario.Step{{
			Name: "step",
			Request: scenario.Request{
				Model:     "claude-haiku",
				MaxTokens: 50,
				Messages:  []scenario.Message{{Role: "user", Content: "please run this"}},
				Tools:     []scenario.Tool{{Name: "shell_exec", Description: "run a shell command"}},
			},
			Expect: scenario.Expect{Status: http.StatusOK},
		}},
	}
	Run(s, srv.URL, "")

	if gotBody == nil {
		t.Fatal("gateway never received a request body")
	}
	if gotBody["model"] != "claude-haiku" {
		t.Errorf("body.model = %v", gotBody["model"])
	}
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("body.tools = %v", gotBody["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["name"] != "shell_exec" {
		t.Errorf("tools[0] = %v", tools[0])
	}
}

func TestRunMetricsSumsCostAcrossCalls(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusOK, map[string]string{"x-fuse-cost-usd": "0.001000"}
	})

	// Expect 402, which never matches a 200 response, so every repeat runs
	// and its cost is summed.
	res := Run(runawayScenario(3, 3), srv.URL, "")
	if res.Metrics.Calls != 3 {
		t.Fatalf("Calls = %d, want 3", res.Metrics.Calls)
	}
	const want = 0.003
	if diff := res.Metrics.BudgetBurnedUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("BudgetBurnedUSD = %v, want %v", res.Metrics.BudgetBurnedUSD, want)
	}
}

func TestRunMetricsIgnoreMissingCostHeader(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusPaymentRequired, nil // blocked call: no cost header
	})

	res := Run(runawayScenario(1, 1), srv.URL, "")
	if res.Metrics.BudgetBurnedUSD != 0 {
		t.Errorf("BudgetBurnedUSD = %v, want 0", res.Metrics.BudgetBurnedUSD)
	}
}

func TestRunMultiStepScenarioAggregatesAcrossSteps(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusOK, map[string]string{"x-fuse-wardryx": "allow"}
	})

	s := wardryxScenario()
	s.Steps = append(s.Steps, scenario.Step{
		Name: "second-attempt",
		Request: scenario.Request{
			Model:    "claude-haiku",
			Messages: []scenario.Message{{Role: "user", Content: "try again"}},
		},
		Expect: scenario.Expect{Status: http.StatusForbidden, Header: map[string]string{"x-fuse-wardryx": "deny"}},
	})

	res := Run(s, srv.URL, "")
	if res.Status != StatusFailed {
		t.Fatalf("Status = %q, want failed", res.Status)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("Findings = %d, want 2 (one per step)", len(res.Findings))
	}
	if res.Metrics.Calls != 2 {
		t.Errorf("Calls = %d, want 2", res.Metrics.Calls)
	}
}

// approvalRequiredScenario mirrors scenarios/approval-required.yaml: a
// high-cost action with no approval_token attached, which wardryx's PDP
// (internal/pdp/pdp.go's Decide, in the wardryx repo) resolves to Hold
// once a matched policy's require_human_above_usd is exceeded and no
// token is presented. The gateway wraps a Hold via wardryx_hold_response
// (tokenfuse's proxy.rs): 403 with x-fuse-wardryx: hold.
func approvalRequiredScenario() scenario.Scenario {
	return scenario.Scenario{
		Name:     "approval-required",
		Requires: "wardryx",
		Steps: []scenario.Step{{
			Name: "high-cost-action-no-token",
			Request: scenario.Request{
				Model:     "claude-opus-4-5",
				MaxTokens: 500000000,
				Messages:  []scenario.Message{{Role: "user", Content: "please go ahead and process this transaction now"}},
			},
			Expect: scenario.Expect{
				Status: http.StatusForbidden,
				Header: map[string]string{"x-fuse-wardryx": "hold"},
			},
		}},
	}
}

func TestRunApprovalRequiredPassesOnHold(t *testing.T) {
	// The x-fuse-approval-id value is minted fresh per hold on the real
	// gateway, so it is sent here for realism but never asserted by
	// Expect: only the stable x-fuse-wardryx: hold signal is.
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusForbidden, map[string]string{
			"x-fuse-wardryx":     "hold",
			"x-fuse-approval-id": "appr-test-1",
		}
	})

	res := Run(approvalRequiredScenario(), srv.URL, "")
	if res.Status != StatusPassed {
		t.Errorf("Status = %q, want passed; findings = %+v", res.Status, res.Findings)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", res.Findings)
	}
}

func TestRunApprovalRequiredFindingWhenBypassed(t *testing.T) {
	// Wardryx is clearly wired in: it stamps x-fuse-wardryx on this call,
	// but let a high-cost, tokenless action straight through instead of
	// holding it for a human. That is a genuine gap, not a missing feature.
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusOK, map[string]string{"x-fuse-wardryx": "allow"}
	})

	res := Run(approvalRequiredScenario(), srv.URL, "")
	if res.Status != StatusFailed {
		t.Fatalf("Status = %q, want failed", res.Status)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want 1", res.Findings)
	}
	if res.Findings[0].GotStatus != http.StatusOK {
		t.Errorf("Finding.GotStatus = %d, want 200", res.Findings[0].GotStatus)
	}
	if res.Findings[0].ExpectStatus != http.StatusForbidden {
		t.Errorf("Finding.ExpectStatus = %d, want 403", res.Findings[0].ExpectStatus)
	}
}

// onBehalfOfForgedChainScenario mirrors
// scenarios/on-behalf-of-forged-chain.yaml: a cyclic on_behalf_of chain
// (the same principal twice) fails agent-stack-go's chain.Validate inside
// wardryx's Decide, independent of any matched policy, and denies
// outright. The gateway wraps that Deny via wardryx_deny_response, the
// same function wardryxScenario above exercises: 403 with
// x-fuse-wardryx: deny.
func onBehalfOfForgedChainScenario() scenario.Scenario {
	return scenario.Scenario{
		Name:     "on-behalf-of-forged-chain",
		Requires: "wardryx",
		Steps: []scenario.Step{{
			Name: "cyclic-delegation-chain",
			Request: scenario.Request{
				Model:    "claude-haiku",
				Messages: []scenario.Message{{Role: "user", Content: "please process this on behalf of the account holder"}},
			},
			Headers: scenario.Headers{
				OnBehalfOf: "user://test/operator,agent://test/orchestrator,agent://test/orchestrator",
			},
			Expect: scenario.Expect{
				Status: http.StatusForbidden,
				Header: map[string]string{"x-fuse-wardryx": "deny"},
			},
		}},
	}
}

func TestRunOnBehalfOfForgedChainPassesOnDeny(t *testing.T) {
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusForbidden, map[string]string{"x-fuse-wardryx": "deny"}
	})

	res := Run(onBehalfOfForgedChainScenario(), srv.URL, "")
	if res.Status != StatusPassed {
		t.Errorf("Status = %q, want passed; findings = %+v", res.Status, res.Findings)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", res.Findings)
	}
}

func TestRunOnBehalfOfForgedChainFindingWhenBypassed(t *testing.T) {
	// Wardryx is clearly wired in: it stamps x-fuse-wardryx on this call,
	// but let a forged, self-referential delegation chain straight through
	// instead of rejecting it. That is a genuine gap, not a missing
	// feature.
	srv := newStubGateway(t, func(call int, r *http.Request, body map[string]any) (int, map[string]string) {
		return http.StatusOK, map[string]string{"x-fuse-wardryx": "allow"}
	})

	res := Run(onBehalfOfForgedChainScenario(), srv.URL, "")
	if res.Status != StatusFailed {
		t.Fatalf("Status = %q, want failed", res.Status)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want 1", res.Findings)
	}
	if res.Findings[0].GotStatus != http.StatusOK {
		t.Errorf("Finding.GotStatus = %d, want 200", res.Findings[0].GotStatus)
	}
	if res.Findings[0].ExpectStatus != http.StatusForbidden {
		t.Errorf("Finding.ExpectStatus = %d, want 403", res.Findings[0].ExpectStatus)
	}
}
