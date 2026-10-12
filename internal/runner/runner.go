// Package runner sends a scenario's crafted requests to a gateway and
// asserts the guardrail response the scenario declared it expects.
//
// This is the execution engine of mockryx, a defensive self-test harness:
// every request it sends targets the one gateway URL an operator supplies,
// which in normal use is the operator's own pre-production TokenFuse
// gateway, running in front of a fake or echo model provider, and, for an mcp
// step, the one MCP broker URL the operator supplies with --mcp-broker.
// Runner never discovers, guesses, or defaults a target; it only ever calls
// the URLs it is explicitly given, so a rehearsal cannot accidentally reach
// anything beyond the sandbox it was pointed at.
package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/mockryx/internal/scenario"
)

// Watcher looks for a downstream, off-path service's own agent-event
// reaction to one step's crafted request, correlated by the run_id sent
// on the wire as x-fuse-run-id. sentAt is the instant that request went
// out: an implementation must not accept a match timestamped before it, so
// a line already sitting in the watched log before this attempt ever fired
// cannot stand in for a genuine reaction to it. The fourth return value is
// a diagnostic only, not a control signal: how many lines matched
// source/type/run_id but were refused on time alone, so a caller building
// a "no event observed" Finding can say whether it saw near-misses or
// nothing at all. See package watch's FileWatcher for the concrete,
// file-polling implementation (which also verifies the watched file's
// integrity chain before trusting a match); this interface exists so this
// package's own tests can use an in-memory fake instead of real files.
type Watcher interface {
	Wait(runID, source, eventType string, sentAt time.Time, timeout time.Duration) (ev event.Event, ok bool, refusedOnTime int, err error)
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
	// Kind, Expect and Got describe a gap in a tool_check or an mcp step, the
	// two step kinds that have no HTTP status to compare. Kind is empty for an
	// ordinary messages step, whose gap is described by the status and header
	// fields above, so a report written before these existed reads the same.
	Kind string `json:"kind,omitempty"`
	// Expect is what the step asked for, e.g. `decision "deny"` or
	// `JSON-RPC error code -32002`; Got is what came back.
	Expect string `json:"expect,omitempty"`
	Got    string `json:"got,omitempty"`
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
	return RunWithBroker(s, gatewayURL, "", apiKey, watcher)
}

// RunWithBroker is Run for a scenario that may also carry mcp steps, which are
// sent to brokerURL, the MCP broker the operator named with --mcp-broker. An
// mcp step with an empty brokerURL is a hard Finding, never a skip: cmd/mockryx
// refuses to start in that state, so reaching it means a caller skipped that
// check, and the loud outcome is the right one.
func RunWithBroker(s scenario.Scenario, gatewayURL, brokerURL, apiKey string, watcher Watcher) Result {
	client := &http.Client{Timeout: httpTimeout}
	tgt := target{gateway: gatewayURL, broker: brokerURL, apiKey: apiKey}

	res := Result{Scenario: s.Name}
	var findings []Finding
	signalSeen := s.Requires == ""
	hardFailure := false

	// One run_id for the whole scenario, minted here rather than per step, and
	// unique to this invocation.
	//
	// Shared across steps because a drill that chases a reaction through more
	// than one plane needs its steps to be one run: the crafted request is one
	// step and the downstream record it should produce is watched by the next,
	// and the watcher correlates on exactly this value. A per-step id made that
	// scenario impossible to express without pinning a constant.
	//
	// Unique per invocation because the journals these drills read are
	// append-only. Under a pinned constant, a run whose reaction never came
	// still finds the PREVIOUS run's record sitting in the file and reports a
	// pass, so the drill stops being able to fail. A step may still pin its own
	// id and keep it verbatim, which is what a fixture replaying known bytes
	// needs; it inherits that hazard along with the control.
	runID := fmt.Sprintf("mockryx-%s-%d", sanitizeForID(s.Name), time.Now().UnixNano())

	for _, step := range s.Steps {
		out := runStep(client, watcher, tgt, s.Requires, runID, step)
		res.Metrics.Calls += out.calls
		res.Metrics.BudgetBurnedUSD += out.budgetBurnedUSD
		if out.signalSeen {
			signalSeen = true
		}
		if out.transportError || out.watcherError || out.unreadable {
			// A watcher read error (e.g. permission denied) is an
			// operational problem, not a "guardrail absent" situation --
			// same treatment as a transport error, never confused with
			// StatusSkipped. So is a reply that cannot be read as the
			// protocol the step speaks (a check-tool-call answer with no
			// decision, a broker answer that is not JSON-RPC): it says
			// nothing about whether the guardrail is configured.
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

// target is where a run's requests go: the gateway every step kind but mcp
// talks to, the MCP broker an mcp step talks to (empty unless the operator
// passed --mcp-broker), and the key sent to the gateway.
type target struct {
	gateway string
	broker  string
	apiKey  string
}

// stepOutcome is runStep's internal bookkeeping, folded into Result by Run.
type stepOutcome struct {
	calls           int
	budgetBurnedUSD float64
	signalSeen      bool
	transportError  bool
	watcherError    bool
	// unreadable: the reply could not be read as the protocol the step speaks.
	unreadable bool
	finding    *Finding
}

// probeResult is what one attempt of one step observed, whatever the step's
// kind: runStep reads these facts and nothing kind-specific.
type probeResult struct {
	// status and hdr are the HTTP response's. For a messages step they are
	// what the expectation is compared with; for the other kinds they are
	// kept for the report only.
	status int
	hdr    http.Header
	// matched: the step's expect held on this attempt.
	matched bool
	// signal: the guardrail's family showed itself on this attempt, which is
	// what separates "not configured" from "configured and did not hold".
	// Messages: the x-fuse-<requires> header is present. tool_check: the
	// firewall answered governed: true. mcp: the broker answered with a
	// JSON-RPC error in the range it reserves for its own guardrails.
	signal bool
	// got describes a tool_check or mcp reply in words, for the report.
	got string
	// err is a transport failure: nothing answered.
	err error
	// unreadable, when set, says why an answer could not be read as the
	// protocol at all.
	unreadable string
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
//
// The step may be of any kind (see scenario.Step.Kind); everything above the
// probe is the same for all three, which is what keeps "a miss is a gap, an
// absent feature is a skip, an unreachable gateway is never a skip" one rule
// instead of three.
func runStep(client *http.Client, watcher Watcher, tgt target, requires, runID string, step scenario.Step) stepOutcome {
	var out stepOutcome

	repeat := step.Repeat
	if repeat <= 0 {
		repeat = 1
	}
	deadline := step.Expect.WithinRepeats
	if deadline <= 0 || deadline > repeat {
		deadline = repeat
	}

	kind := step.Kind()

	var signalHeader string
	if requires != "" && kind == scenario.KindMessages {
		signalHeader = "x-fuse-" + strings.ToLower(requires)
	}

	headers := step.Headers
	if headers.RunID == "" {
		headers.RunID = runID
	}

	if kind == scenario.KindMCP && tgt.broker == "" {
		out.unreadable = true
		out.finding = newFinding(kind, step, 1, probeResult{},
			"this is an mcp step but no MCP broker is configured for this run: set --mcp-broker or $MOCKRYX_MCP_BROKER")
		return out
	}

	var last probeResult
	for attempt := 1; attempt <= repeat; attempt++ {
		// Captured before the request goes out, not after the response comes
		// back: this is the instant a downstream reaction's own timestamp
		// must be at or after to count as evidence about THIS attempt, not
		// some earlier one. See package watch's Wait.
		sentAt := time.Now()
		p := probe(client, tgt, step, kind, headers, signalHeader, attempt)
		out.calls++
		if p.err != nil {
			out.transportError = true
			out.finding = newFinding(kind, step, attempt, probeResult{}, fmt.Sprintf("request failed: %v", p.err))
			return out
		}
		if p.unreadable != "" {
			out.unreadable = true
			out.finding = newFinding(kind, step, attempt, p, p.unreadable)
			return out
		}
		out.budgetBurnedUSD += costUSD(p.hdr)
		if p.signal {
			out.signalSeen = true
		}
		last = p

		if p.matched {
			if attempt <= deadline {
				if step.Expect.Event != nil {
					ev := step.Expect.Event
					if watcher == nil {
						out.watcherError = true
						f := newFinding(kind, step, attempt, p,
							fmt.Sprintf("step declares expect.event (%s/%s) but no Watcher was configured for this run", ev.Source, ev.Type))
						f.ExpectEventSource, f.ExpectEventType = ev.Source, ev.Type
						out.finding = f
						return out
					}
					_, ok, refusedOnTime, err := watcher.Wait(headers.RunID, ev.Source, ev.Type, sentAt, ev.Within)
					if err != nil {
						out.watcherError = true
						f := newFinding(kind, step, attempt, p,
							fmt.Sprintf("watch %s/%s for run %s: %v", ev.Source, ev.Type, headers.RunID, err))
						f.ExpectEventSource, f.ExpectEventType = ev.Source, ev.Type
						out.finding = f
						return out
					}
					if !ok {
						detail := fmt.Sprintf("gateway response matched, but no %s/%s event observed for run %s within %s", ev.Source, ev.Type, headers.RunID, ev.Within)
						if refusedOnTime > 0 {
							// See package watch's Wait: a downstream log is
							// NTP-assumed, not verified, to be in sync with
							// this process's clock. A near-miss here is
							// usually that assumption strained, not an
							// absent guardrail, and worth telling apart.
							detail += fmt.Sprintf(" (%d matching line(s) seen but refused on time: outside the trusted window around this attempt, possibly clock skew)", refusedOnTime)
						}
						f := newFinding(kind, step, attempt, p, detail)
						f.ExpectEventSource, f.ExpectEventType = ev.Source, ev.Type
						out.finding = f
						return out
					}
				}
				return out
			}
			out.finding = newFinding(kind, step, attempt, p,
				fmt.Sprintf("guardrail matched on attempt %d, after within_repeats=%d", attempt, deadline))
			return out
		}
	}

	out.finding = newFinding(kind, step, repeat, last, noMatchDetail(kind, step, deadline, repeat, last))
	return out
}

// noMatchDetail is the sentence for a step that never matched. The messages
// form is the one every report has always carried and is unchanged.
func noMatchDetail(kind scenario.StepKind, step scenario.Step, deadline, repeat int, last probeResult) string {
	if kind == scenario.KindMessages {
		return fmt.Sprintf("expected status %d within %d attempt(s), got %d after %d attempt(s)",
			step.Expect.Status, deadline, last.status, repeat)
	}
	// Expect and Got already say what was wanted and what came back, and the
	// report prints both on the same line, so this sentence only adds the
	// attempts.
	return fmt.Sprintf("not met within %d attempt(s), tried %d", deadline, repeat)
}

// newFinding builds a Finding for step at attempt from what p observed. A
// messages step keeps the status and header fields it always had; the other
// kinds fill Kind, Expect and Got instead, since they have no status to expect.
func newFinding(kind scenario.StepKind, step scenario.Step, attempt int, p probeResult, detail string) *Finding {
	f := &Finding{
		Step:       step.Name,
		Attempt:    attempt,
		GotStatus:  p.status,
		GotHeaders: flatten(p.hdr),
		Detail:     detail,
	}
	if kind == scenario.KindMessages {
		f.ExpectStatus = step.Expect.Status
		f.ExpectHeader = step.Expect.Header
		return f
	}
	f.Kind = string(kind)
	f.Expect = expectText(step)
	f.Got = p.got
	return f
}

// expectText says what a tool_check or mcp step asked for, in words.
func expectText(step scenario.Step) string {
	switch step.Kind() {
	case scenario.KindToolCheck:
		return fmt.Sprintf("decision %q", step.Expect.Decision)
	case scenario.KindMCP:
		if step.Expect.ErrorCode != nil {
			t := fmt.Sprintf("JSON-RPC error code %d", *step.Expect.ErrorCode)
			if step.Expect.ErrorContains != "" {
				t += fmt.Sprintf(" containing %q", step.Expect.ErrorContains)
			}
			return t
		}
		return "a JSON-RPC result"
	}
	return fmt.Sprintf("status %d", step.Expect.Status)
}

// probe makes one attempt of step and reports what it observed.
func probe(client *http.Client, tgt target, step scenario.Step, kind scenario.StepKind, h scenario.Headers, signalHeader string, attempt int) probeResult {
	switch kind {
	case scenario.KindToolCheck:
		return probeToolCheck(client, tgt, step, h)
	case scenario.KindMCP:
		return probeMCP(client, tgt, step, h, attempt)
	}
	status, hdr, err := send(client, tgt.gateway, tgt.apiKey, step.Request, h)
	if err != nil {
		return probeResult{err: err}
	}
	return probeResult{
		status:  status,
		hdr:     hdr,
		matched: matches(step.Expect, status, hdr),
		signal:  signalHeader != "" && hdr.Get(signalHeader) != "",
	}
}

// maxReply bounds how much of a reply the new step kinds read.
const maxReply = 1 << 20

// checkToolCallPath is the gateway route a tool_check step asks.
const checkToolCallPath = "/v1/fuse/check-tool-call"

// probeToolCheck asks the gateway whether step's tool may run in h.RunID.
//
// The answer is read for exactly two things. "decision" is compared with the
// expectation. "governed" is the signal: the firewall's own statement that it
// judged the call, which an off firewall answers false. That is the one honest
// way to tell an off firewall from an enforcing one that let a call through,
// and it is why a tool_check scenario must declare requires: taint.
func probeToolCheck(client *http.Client, tgt target, step scenario.Step, h scenario.Headers) probeResult {
	body, err := json.Marshal(struct {
		RunID string `json:"run_id"`
		Tool  string `json:"tool"`
	}{h.RunID, step.ToolCheck.Tool})
	if err != nil {
		return probeResult{err: fmt.Errorf("encode request: %w", err)}
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(tgt.gateway, "/")+checkToolCallPath, bytes.NewReader(body))
	if err != nil {
		return probeResult{err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("content-type", "application/json")
	setHeader(req, "x-api-key", tgt.apiKey)
	setHeader(req, "x-fuse-agent-id", h.AgentID)

	resp, err := client.Do(req)
	if err != nil {
		return probeResult{err: fmt.Errorf("send request: %w", err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxReply))

	p := probeResult{status: resp.StatusCode, hdr: resp.Header}
	var ans struct {
		Decision   string  `json:"decision"`
		Governed   bool    `json:"governed"`
		WouldBlock *string `json:"would_block"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &ans) != nil || ans.Decision == "" {
		p.unreadable = fmt.Sprintf("POST %s answered HTTP %d with no decision in it (%s); a gateway older than that endpoint answers 404",
			checkToolCallPath, resp.StatusCode, snippet(raw))
		return p
	}
	p.got = fmt.Sprintf("decision %q (governed: %t)", ans.Decision, ans.Governed)
	if ans.WouldBlock != nil && *ans.WouldBlock != "" {
		p.got += ", would block: " + *ans.WouldBlock
	}
	p.matched = ans.Decision == step.Expect.Decision
	p.signal = ans.Governed
	return p
}

// The JSON-RPC error codes TokenFuse's MCP broker reserves for its own
// guardrails run -32001 (poisoned tool description) to -32010 (session). A
// code in that range is the broker speaking; a code outside it (-32602,
// invalid params, is the usual one) is an upstream's own.
const (
	brokerCodeLow  = -32010
	brokerCodeHigh = -32001
)

// probeMCP sends one JSON-RPC 2.0 request to the broker and reads the reply.
//
// The broker stamps no x-fuse-* header on a reply, so the signal here is the
// reply itself: a JSON-RPC error whose code is one the broker reserves. A
// result, or an upstream's own error, announces nothing. So a broker whose
// scan is off, and a broker whose scan is on but failed, answer the same way,
// and both read as "not configured", which --fail-on-skip turns into a gap
// for an operator who knows the scan must be on. README says so in the same
// words.
func probeMCP(client *http.Client, tgt target, step scenario.Step, h scenario.Headers, attempt int) probeResult {
	body, err := json.Marshal(struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      int            `json:"id"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params,omitempty"`
	}{"2.0", attempt, step.MCP.Method, step.MCP.Params})
	if err != nil {
		return probeResult{err: fmt.Errorf("encode request: %w", err)}
	}
	req, err := http.NewRequest(http.MethodPost, tgt.broker, bytes.NewReader(body))
	if err != nil {
		return probeResult{err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	setHeader(req, "x-fuse-agent-id", h.AgentID)
	setHeader(req, "x-fuse-run-id", h.RunID)

	resp, err := client.Do(req)
	if err != nil {
		return probeResult{err: fmt.Errorf("send request: %w", err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxReply))

	p := probeResult{status: resp.StatusCode, hdr: resp.Header}
	// An "error" is a JSON-RPC error only if it carries an integer code. The
	// broker's own door refusal (a 401 for a caller with no key) is an "error"
	// object too, with a type and a reason and no code, and reading it as an
	// error with code 0 reported a broker that had refused to talk as one
	// that was not configured.
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    *int   `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &rpc) != nil || (rpc.Error == nil && len(rpc.Result) == 0) ||
		(rpc.Error != nil && rpc.Error.Code == nil) {
		p.unreadable = fmt.Sprintf("the MCP broker answered HTTP %d with something that is not a JSON-RPC reply (%s)",
			resp.StatusCode, snippet(raw))
		return p
	}

	if rpc.Error != nil {
		code := *rpc.Error.Code
		p.got = fmt.Sprintf("JSON-RPC error %d: %s", code, snippet([]byte(rpc.Error.Message)))
		p.signal = code >= brokerCodeLow && code <= brokerCodeHigh
		if want := step.Expect.ErrorCode; want != nil {
			p.matched = code == *want &&
				(step.Expect.ErrorContains == "" || strings.Contains(rpc.Error.Message, step.Expect.ErrorContains))
		}
		return p
	}
	p.got = "a JSON-RPC result"
	p.matched = step.Expect.Result
	return p
}

// ProbeBroker reports whether anything answers at the MCP broker's host. It
// asks GET /healthz at the URL's origin and treats ANY HTTP answer as
// "reachable", a 404 from a proxy in front of the broker included: the
// question is whether a connection can be made, not whether the path exists.
// cmd/mockryx calls it before sending anything when an mcp step is loaded, so
// a broker that is not there is a usage error and not a run of transport
// Findings that proved nothing.
func ProbeBroker(brokerURL string) error {
	u, err := url.Parse(brokerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not an http or https URL", brokerURL)
	}
	req, err := http.NewRequest(http.MethodGet, (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/healthz"}).String(), nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// snippet makes a reply's body fit one line of a report: whitespace folded,
// cut at 120 bytes.
func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	if s == "" {
		return "empty body"
	}
	return s
}

// send POSTs one crafted request to {gatewayURL}/v1/messages and returns
// the response status and headers.
func send(client *http.Client, gatewayURL, apiKey string, req scenario.Request, h scenario.Headers) (int, http.Header, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, nil, fmt.Errorf("encode request: %w", err)
	}

	endpoint := strings.TrimRight(gatewayURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
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
