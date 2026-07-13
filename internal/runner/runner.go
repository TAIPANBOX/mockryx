// Package runner sends a scenario's crafted requests to a gateway and
// asserts the guardrail response the scenario declared it expects.
//
// This is the execution engine of mockryx, a defensive self-test harness:
// every request it sends targets the one gateway URL an operator supplies,
// which in normal use is the operator's own pre-production TokenFuse
// gateway, running in front of a fake or echo model provider. Runner never
// discovers, guesses, or defaults a target; it only ever calls the URL it
// is explicitly given, so a rehearsal cannot accidentally reach anything
// beyond the sandbox it was pointed at.
package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/mockryx/internal/scenario"
)

// Watcher looks for a downstream, off-path service's own agent-event
// reaction to one step's crafted request, correlated by the run_id sent
// on the wire as x-fuse-run-id. See package watch's FileWatcher for the
// concrete, file-polling implementation; this interface exists so this
// package's own tests can use an in-memory fake instead of real files.
type Watcher interface {
	Wait(runID, source, eventType string, timeout time.Duration) (event.Event, bool, error)
}

// Status is the outcome of running one scenario end to end.
type Status string

const (
	// StatusPassed means every step matched its expected guardrail
	// response, within its within_repeats deadline.
	StatusPassed Status = "passed"
	// StatusFailed means at least one step did not get the guardrail
	// response it expected, and the gap could not be explained away by a
	// missing optional feature: either the scenario has no Requires, its
	// signal header was observed elsewhere in the run, or the gateway
	// could not even be reached. This is mockryx's "go fix this" signal.
	StatusFailed Status = "failed"
	// StatusSkipped means the scenario declared Requires, but that
	// feature's "x-fuse-<requires>" signal header was never once observed
	// across every attempt, on any response. The gateway plainly does not
	// have the feature wired in, so mockryx reports this separately from
	// StatusFailed to avoid a false failure against a guardrail that was
	// never configured in the first place.
	StatusSkipped Status = "skipped_not_configured"
)

// Finding is a defensive gap: a step did not get the guardrail response its
// scenario declared, even though nothing suggests the guardrail's feature
// is simply absent from the gateway. This is the harness's primary output:
// something to fix in the operator's own guardrail configuration before a
// real incident finds it first.
type Finding struct {
	Scenario     string            `json:"scenario"`
	Step         string            `json:"step"`
	Attempt      int               `json:"attempt"`
	ExpectStatus int               `json:"expect_status"`
	ExpectHeader map[string]string `json:"expect_header,omitempty"`
	GotStatus    int               `json:"got_status"`
	GotHeaders   map[string]string `json:"got_headers,omitempty"`
	Detail       string            `json:"detail"`
	// ExpectEventSource and ExpectEventType are set only when this Finding
	// stems from a failed Expect.Event check (the synchronous status/
	// header assertion above already passed, but the downstream reaction
	// never appeared) -- never together with a synchronous mismatch, so a
	// reader can tell the two kinds of gap apart at a glance. See package
	// watch.
	ExpectEventSource string `json:"expect_event_source,omitempty"`
	ExpectEventType   string `json:"expect_event_type,omitempty"`
}

// Metrics is a blast-radius proxy for one scenario: how much the runner
// actually did against the gateway before the guardrail, or the repeat
// budget, stopped it.
type Metrics struct {
	Calls           int     `json:"calls"`
	BudgetBurnedUSD float64 `json:"budget_burned_usd"`
}

// Result is the outcome of running one scenario.
type Result struct {
	Scenario string    `json:"scenario"`
	Status   Status    `json:"status"`
	Findings []Finding `json:"findings,omitempty"`
	// SkippedFindings holds the step mismatches that triggered StatusSkipped,
	// discarded from Findings because the scenario's declared guardrail was
	// never observed active. A mismatch here looks, from the wire alone,
	// exactly like what a guardrail that IS configured but badly broken
	// would also produce: absent and broken are indistinguishable without
	// the signal header. SkippedFindings never counts toward a defensive
	// gap and never affects exit code by default; it exists so a human
	// reading the report can still see the near-miss, and so an operator
	// who knows the guardrail must be present can promote it to a hard
	// failure with --fail-on-skip (see cmd/mockryx). Populated only when
	// Status is StatusSkipped, in which case it always has at least one
	// entry.
	SkippedFindings []Finding `json:"skipped_findings,omitempty"`
	Metrics         Metrics   `json:"metrics"`
}

// httpTimeout bounds every request the runner sends, so an unresponsive
// gateway cannot hang a rehearsal indefinitely.
const httpTimeout = 15 * time.Second

// Run sends s's steps to gatewayURL, in order, and asserts the guardrail
// response each declares in its Expect block. apiKey, when non-empty, is
// sent as x-api-key on every request. watcher checks any step's
// Expect.Event after its synchronous assertion passes; it may be nil only
// when no step in s declares one -- callers (cmd/mockryx) are expected to
// validate that upfront across every scenario in a run, before calling Run
// at all, rather than have Run guess what a nil watcher should mean for one
// specific step.
//
// It returns the scenario's Result. Status is StatusFailed when any step
// produced a Finding and the gap cannot be attributed to a missing optional
// feature; StatusSkipped when the scenario's declared Requires feature was
// never once observed active; and StatusPassed otherwise. Findings is only
// populated for StatusFailed: a StatusSkipped result deliberately drops its
// raw mismatches from Findings, since by default they reflect an absent
// feature, not a defensive gap to fix. Those same raw mismatches are never
// discarded outright, though: they land on SkippedFindings instead, so nothing
// is silently thrown away.
func Run(s scenario.Scenario, gatewayURL, apiKey string, watcher Watcher) Result {
	client := &http.Client{Timeout: httpTimeout}

	res := Result{Scenario: s.Name}
	var findings []Finding
	signalSeen := s.Requires == ""
	hardFailure := false

	for _, step := range s.Steps {
		out := runStep(client, watcher, gatewayURL, apiKey, s.Requires, step)
		res.Metrics.Calls += out.calls
		res.Metrics.BudgetBurnedUSD += out.budgetBurnedUSD
		if out.signalSeen {
			signalSeen = true
		}
		if out.transportError || out.watcherError {
			// A watcher read error (e.g. permission denied) is an
			// operational problem, not a "guardrail absent" situation --
			// same treatment as a transport error, never confused with
			// StatusSkipped.
			hardFailure = true
		}
		if out.finding != nil {
			out.finding.Scenario = s.Name
			findings = append(findings, *out.finding)
		}
	}

	switch {
	case len(findings) == 0:
		res.Status = StatusPassed
	case !hardFailure && !signalSeen:
		// A transport error is never read as "not configured": failing to
		// even reach the gateway is always a hard failure, regardless of
		// what optional feature the scenario declares.
		res.Status = StatusSkipped
		res.SkippedFindings = findings
	default:
		res.Status = StatusFailed
		res.Findings = findings
	}
	return res
}

// stepOutcome is runStep's internal bookkeeping, folded into Result by Run.
type stepOutcome struct {
	calls           int
	budgetBurnedUSD float64
	signalSeen      bool
	transportError  bool
	watcherError    bool
	finding         *Finding
}

// runStep sends step's request up to step.Repeat times, reusing one run_id
// across every attempt (the budget Breaker and similar guardrails key
// their state off it, so a fresh run_id per attempt would reset that
// state and the guardrail could never trip). It stops at the first
// attempt whose response matches step.Expect: a pass if that happened at
// or before step.Expect.WithinRepeats -- and, if step.Expect.Event is set,
// watcher also observes a matching downstream reaction within Event.Within
// -- otherwise a Finding for firing too late. If no attempt ever matches,
// it returns a Finding after step.Repeat attempts.
func runStep(client *http.Client, watcher Watcher, gatewayURL, apiKey, requires string, step scenario.Step) stepOutcome {
	var out stepOutcome

	repeat := step.Repeat
	if repeat <= 0 {
		repeat = 1
	}
	deadline := step.Expect.WithinRepeats
	if deadline <= 0 || deadline > repeat {
		deadline = repeat
	}

	var signalHeader string
	if requires != "" {
		signalHeader = "x-fuse-" + strings.ToLower(requires)
	}

	headers := step.Headers
	if headers.RunID == "" {
		headers.RunID = fmt.Sprintf("mockryx-%s-%d", sanitizeForID(step.Name), time.Now().UnixNano())
	}

	var lastStatus int
	var lastHeaders http.Header
	for attempt := 1; attempt <= repeat; attempt++ {
		status, hdr, err := send(client, gatewayURL, apiKey, step.Request, headers)
		out.calls++
		if err != nil {
			out.transportError = true
			out.finding = &Finding{
				Step:         step.Name,
				Attempt:      attempt,
				ExpectStatus: step.Expect.Status,
				ExpectHeader: step.Expect.Header,
				Detail:       fmt.Sprintf("request failed: %v", err),
			}
			return out
		}
		out.budgetBurnedUSD += costUSD(hdr)
		if signalHeader != "" && hdr.Get(signalHeader) != "" {
			out.signalSeen = true
		}
		lastStatus, lastHeaders = status, hdr

		if matches(step.Expect, status, hdr) {
			if attempt <= deadline {
				if step.Expect.Event != nil {
					ev := step.Expect.Event
					if watcher == nil {
						out.watcherError = true
						out.finding = &Finding{
							Step:              step.Name,
							Attempt:           attempt,
							ExpectStatus:      step.Expect.Status,
							ExpectHeader:      step.Expect.Header,
							GotStatus:         status,
							GotHeaders:        flatten(hdr),
							ExpectEventSource: ev.Source,
							ExpectEventType:   ev.Type,
							Detail:            fmt.Sprintf("step declares expect.event (%s/%s) but no Watcher was configured for this run", ev.Source, ev.Type),
						}
						return out
					}
					_, ok, err := watcher.Wait(headers.RunID, ev.Source, ev.Type, ev.Within)
					if err != nil {
						out.watcherError = true
						out.finding = &Finding{
							Step:              step.Name,
							Attempt:           attempt,
							ExpectStatus:      step.Expect.Status,
							ExpectHeader:      step.Expect.Header,
							GotStatus:         status,
							GotHeaders:        flatten(hdr),
							ExpectEventSource: ev.Source,
							ExpectEventType:   ev.Type,
							Detail:            fmt.Sprintf("watch %s/%s for run %s: %v", ev.Source, ev.Type, headers.RunID, err),
						}
						return out
					}
					if !ok {
						out.finding = &Finding{
							Step:              step.Name,
							Attempt:           attempt,
							ExpectStatus:      step.Expect.Status,
							ExpectHeader:      step.Expect.Header,
							GotStatus:         status,
							GotHeaders:        flatten(hdr),
							ExpectEventSource: ev.Source,
							ExpectEventType:   ev.Type,
							Detail:            fmt.Sprintf("gateway response matched, but no %s/%s event observed for run %s within %s", ev.Source, ev.Type, headers.RunID, ev.Within),
						}
						return out
					}
				}
				return out
			}
			out.finding = &Finding{
				Step:         step.Name,
				Attempt:      attempt,
				ExpectStatus: step.Expect.Status,
				ExpectHeader: step.Expect.Header,
				GotStatus:    status,
				GotHeaders:   flatten(hdr),
				Detail:       fmt.Sprintf("guardrail matched on attempt %d, after within_repeats=%d", attempt, deadline),
			}
			return out
		}
	}

	out.finding = &Finding{
		Step:         step.Name,
		Attempt:      repeat,
		ExpectStatus: step.Expect.Status,
		ExpectHeader: step.Expect.Header,
		GotStatus:    lastStatus,
		GotHeaders:   flatten(lastHeaders),
		Detail: fmt.Sprintf("expected status %d within %d attempt(s), got %d after %d attempt(s)",
			step.Expect.Status, deadline, lastStatus, repeat),
	}
	return out
}

// send POSTs one crafted request to {gatewayURL}/v1/messages and returns
// the response status and headers.
func send(client *http.Client, gatewayURL, apiKey string, req scenario.Request, h scenario.Headers) (int, http.Header, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, nil, fmt.Errorf("encode request: %w", err)
	}

	url := strings.TrimRight(gatewayURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	setHeader(httpReq, "x-api-key", apiKey)
	setHeader(httpReq, "x-fuse-run-id", h.RunID)
	setHeader(httpReq, "x-fuse-agent-id", h.AgentID)
	setHeader(httpReq, "x-fuse-budget-usd", h.BudgetUSD)
	setHeader(httpReq, "x-fuse-task-type", h.TaskType)
	setHeader(httpReq, "x-fuse-on-behalf-of", h.OnBehalfOf)
	setHeader(httpReq, "x-fuse-outcome", h.Outcome)
	setHeader(httpReq, "x-fuse-approval-token", h.ApprovalToken)

	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused

	return resp.StatusCode, resp.Header, nil
}

func setHeader(r *http.Request, key, value string) {
	if value != "" {
		r.Header.Set(key, value)
	}
}

// matches reports whether status and hdr satisfy e: an exact status match,
// and an exact value match for every header e.Header names. Header lookups
// are case-insensitive, per HTTP (http.Header.Get canonicalizes the key).
func matches(e scenario.Expect, status int, hdr http.Header) bool {
	if status != e.Status {
		return false
	}
	for k, v := range e.Header {
		if hdr.Get(k) != v {
			return false
		}
	}
	return true
}

// costUSD extracts the gateway's per-call cost signal (x-fuse-cost-usd):
// the actual dollar cost of this one call. Absent or unparsable, which is
// the normal case for a blocked call that never reached a priced upstream,
// contributes 0, never an error: cost accounting here is a best-effort
// blast-radius proxy, not a correctness assertion.
func costUSD(hdr http.Header) float64 {
	v := hdr.Get("x-fuse-cost-usd")
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}

// flatten takes the first value of every response header into a plain map,
// for Finding.GotHeaders: a point-in-time snapshot for the report, not a
// live http.Header.
func flatten(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k := range h {
		out[k] = h.Get(k)
	}
	return out
}

// sanitizeForID replaces spaces in name so a generated run_id stays one
// HTTP-header-friendly token.
func sanitizeForID(name string) string {
	return strings.ReplaceAll(name, " ", "-")
}
