// Package scenario defines the mockryx scenario file format: a small,
// hand-authored YAML or JSON document describing crafted requests to send
// through an operator's own TokenFuse gateway, and the guardrail response
// expected back.
//
// Mockryx is a defensive self-test harness. Every scenario in this package
// is a fire drill against infrastructure the operator already owns and
// controls, never a probe of a third party. A scenario that embeds
// "hostile" content (a fake secret, a request for a denied tool, a tight
// budget) exists only to replay, in an isolated pre-production sandbox,
// the kind of input the operator's own agents could meet in the wild
// against a fake or echo model provider, so a weakness in the operator's
// own guardrails is caught before a real user ever could trigger it.
package scenario

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Sentinel errors returned by Parse/Load, wrapped with additional context
// via fmt.Errorf's %w verb so callers can branch on failure kind with
// errors.Is rather than string matching.
var (
	ErrMissingName         = errors.New("scenario: missing required field: name")
	ErrNoSteps             = errors.New("scenario: at least one step is required")
	ErrStepMissingName     = errors.New("scenario: step missing required field: name")
	ErrStepMissingModel    = errors.New("scenario: step request missing required field: model")
	ErrStepMissingMessages = errors.New("scenario: step request must have at least one message")
	ErrStepMissingStatus   = errors.New("scenario: step expect missing required field: status")
	ErrEventMissingSource  = errors.New("scenario: step expect.event missing required field: source")
	ErrEventMissingType    = errors.New("scenario: step expect.event missing required field: type")
	ErrUnsupportedExt      = errors.New("scenario: unsupported file extension (want .yaml, .yml, or .json)")
	ErrNoScenarios         = errors.New("scenario: no .yaml, .yml, or .json scenario files found")

	// Added with tool history and the tool_check and mcp step kinds
	// (2026-10-12).
	ErrStepKind             = errors.New("scenario: a step must set exactly one of request, tool_check, mcp")
	ErrUnknownBlockType     = errors.New("scenario: message content block has an unknown type (want text, tool_use or tool_result)")
	ErrBlockToolUse         = errors.New("scenario: a tool_use content block needs an id and a name")
	ErrBlockToolResult      = errors.New("scenario: a tool_result content block needs a tool_use_id")
	ErrToolCheckMissingTool = errors.New("scenario: step tool_check missing required field: tool")
	ErrToolCheckDecision    = errors.New("scenario: a tool_check step's expect.decision must be allow or deny")
	ErrToolCheckNeedsTaint  = errors.New("scenario: a scenario with a tool_check step must declare requires: taint")
	ErrMCPMissingMethod     = errors.New("scenario: step mcp missing required field: method")
	ErrMCPNeedsRequires     = errors.New("scenario: a scenario with an mcp step must declare requires")
	ErrMCPExpect            = errors.New("scenario: an mcp step's expect needs exactly one of error_code or result")
	ErrMCPEvent             = errors.New("scenario: an mcp step cannot declare expect.event")
	ErrExpectKind           = errors.New("scenario: step expect carries a field that belongs to another kind of step")
	ErrHeadersUnused        = errors.New("scenario: a tool_check or mcp step sends only headers.run_id and headers.agent_id")
)

// DefaultEventWithin is how long a step waits for its declared Expect.Event
// to appear when Within is zero or negative.
const DefaultEventWithin = 10 * time.Second

// Scenario is one pre-production fire drill: one or more crafted requests
// run against a gateway, each with the guardrail response the operator
// expects back.
type Scenario struct {
	// Name identifies the scenario in reports and events.
	Name string `yaml:"name" json:"name"`
	// Description is a human-readable explanation of what the scenario
	// rehearses and why. Optional, but strongly recommended.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Requires names the optional gateway feature this scenario's
	// guardrail depends on, e.g. "wardryx" or "dlp". Leave it empty for a
	// core, always-on guardrail (e.g. the budget Breaker), where a
	// mismatch is always a genuine Finding.
	//
	// When set, the runner also watches for the guardrail showing itself, and
	// if it never once does across every attempt, the gateway plainly does
	// not have the feature wired in, and the runner reports "guardrail not
	// configured" instead of a false failure. What counts as showing itself
	// depends on the step's kind: a messages step watches every response for
	// the "x-fuse-<requires>" header family (lowercased); a tool_check step
	// watches for the firewall answering governed: true, which is why a
	// scenario with one must say requires: taint; an mcp step watches for a
	// JSON-RPC error in the range the broker reserves for its guardrails
	// (-32001 to -32010), and a scenario with one must say some requires.
	Requires string `yaml:"requires,omitempty" json:"requires,omitempty"`
	// Steps are the crafted requests that make up this scenario, run in
	// order.
	Steps []Step `yaml:"steps" json:"steps"`
}

// Step is one crafted request within a scenario, optionally repeated (for
// runaway/loop scenarios where a guardrail like the budget Breaker only
// trips after a few calls).
type Step struct {
	// Name identifies the step within its scenario.
	Name string `yaml:"name" json:"name"`
	// Repeat is how many times to send this request, in order, stopping
	// early the moment Expect matches. Defaults to 1; Load/Parse normalize
	// a zero or negative value to 1.
	Repeat int `yaml:"repeat,omitempty" json:"repeat,omitempty"`
	// Request is the crafted call sent to POST {gateway}/v1/messages. Exactly
	// one of Request, ToolCheck and MCP is set; see Step.Kind.
	Request Request `yaml:"request,omitempty" json:"request"`
	// ToolCheck asks the gateway's POST /v1/fuse/check-tool-call whether a
	// tool may run in this scenario's run, and asserts the decision.
	ToolCheck *ToolCheck `yaml:"tool_check,omitempty" json:"tool_check,omitempty"`
	// MCP sends one JSON-RPC request to the MCP broker the operator named
	// with --mcp-broker, and asserts a result or a JSON-RPC error.
	MCP *MCPCall `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	// Headers are the x-fuse-* headers sent with the request.
	Headers Headers `yaml:"headers,omitempty" json:"headers,omitempty"`
	// Expect is the guardrail response this step should get back.
	Expect Expect `yaml:"expect" json:"expect"`
}

// Request is the JSON body sent to the gateway's /v1/messages endpoint. Its
// shape deliberately mirrors the Anthropic Messages API that the TokenFuse
// gateway proxies, so a scenario reads like a real call and marshals
// straight onto the wire unchanged.
type Request struct {
	Model     string    `yaml:"model" json:"model"`
	MaxTokens int       `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
	Messages  []Message `yaml:"messages" json:"messages"`
	// Tools optionally names tools the crafted request declares, so a
	// scenario can rehearse a policy denial keyed on tool name (e.g. a
	// Wardryx rule that blocks shell_exec).
	Tools []Tool `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// Message is one entry in Request.Messages, mirroring the Anthropic
// Messages API's {role, content} shape. On that wire a message's content is
// either a plain string or a list of typed blocks, and a scenario may write
// either under `content:`.
//
// Exactly one of Content and Blocks is set after a parse. Content stays a
// plain string, and a plain-string message marshals onto the wire exactly as
// it always did, so no scenario written before blocks existed changes by a
// byte.
type Message struct {
	Role    string `yaml:"role" json:"role"`
	Content string `yaml:"content" json:"content"`
	// Blocks is the list form of content: the text, tool_use and tool_result
	// blocks of a conversation an agent loop has already had. This is how a
	// drill supplies tool HISTORY, which is what the gateway's agent firewall
	// reads taint from (a tool the model invoked, not one a request declares).
	Blocks []Block `yaml:"-" json:"-"`
}

// Block is one structured content block of a Message, in the Anthropic wire
// shape. Type picks which of the other fields apply:
//
//	text         Text
//	tool_use     ID, Name, Input     (a tool the assistant invoked)
//	tool_result  ToolUseID, Content, IsError   (what that tool returned)
//
// Content on a tool_result is the plain-string form only; the list form of a
// tool result's content is not expressible here.
type Block struct {
	Type      string         `yaml:"type" json:"type"`
	Text      string         `yaml:"text,omitempty" json:"text,omitempty"`
	ID        string         `yaml:"id,omitempty" json:"id,omitempty"`
	Name      string         `yaml:"name,omitempty" json:"name,omitempty"`
	Input     map[string]any `yaml:"input,omitempty" json:"input,omitempty"`
	ToolUseID string         `yaml:"tool_use_id,omitempty" json:"tool_use_id,omitempty"`
	Content   string         `yaml:"content,omitempty" json:"content,omitempty"`
	IsError   bool           `yaml:"is_error,omitempty" json:"is_error,omitempty"`
}

// ToolCheck is a tool_check step: it asks the gateway's own
// POST /v1/fuse/check-tool-call whether Tool may run in this scenario's run,
// which is the question an executor asks BEFORE running a tool the model
// requested. The run is the scenario's run_id, so an earlier step that put
// tool history into the run decides the answer.
type ToolCheck struct {
	Tool string `yaml:"tool" json:"tool"`
}

// MCPCall is an mcp step: one JSON-RPC 2.0 request to the MCP broker the
// operator named with --mcp-broker or $MOCKRYX_MCP_BROKER. The broker's
// address is deliberately not a field here: it comes from the command line and
// from nowhere else, the same property CLAUDE.md invariant 6 holds for the
// gateway.
type MCPCall struct {
	// Method is the JSON-RPC method, e.g. tools/call.
	Method string `yaml:"method" json:"method"`
	// Params is the JSON-RPC params object, sent as written.
	Params map[string]any `yaml:"params,omitempty" json:"params,omitempty"`
}

// StepKind names which of the three kinds a Step is.
type StepKind string

const (
	// KindMessages is the original step: a request POSTed to /v1/messages.
	KindMessages StepKind = "messages"
	// KindToolCheck is a POST to /v1/fuse/check-tool-call.
	KindToolCheck StepKind = "tool_check"
	// KindMCP is one JSON-RPC request to the MCP broker.
	KindMCP StepKind = "mcp"
)

// Kind reports which kind the step is, or "" when it sets none or more than
// one of request, tool_check and mcp (which validate refuses).
func (s Step) Kind() StepKind {
	n := 0
	var k StepKind
	if s.Request.isSet() {
		n++
		k = KindMessages
	}
	if s.ToolCheck != nil {
		n++
		k = KindToolCheck
	}
	if s.MCP != nil {
		n++
		k = KindMCP
	}
	if n != 1 {
		return ""
	}
	return k
}

// isSet reports whether the request carries anything at all, which is how a
// step that omitted `request:` is told apart from one that wrote it.
func (r Request) isSet() bool {
	return r.Model != "" || r.MaxTokens != 0 || len(r.Messages) > 0 || len(r.Tools) > 0
}

// Tool is one entry in Request.Tools.
type Tool struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Headers are the x-fuse-* request headers a crafted call carries. Every
// field is optional and, when empty, simply is not sent, with one
// exception: the runner defaults RunID to a generated value when empty,
// since the budget Breaker and other per-run guardrails key their state
// off it.
type Headers struct {
	RunID         string `yaml:"run_id,omitempty" json:"run_id,omitempty"`
	AgentID       string `yaml:"agent_id,omitempty" json:"agent_id,omitempty"`
	BudgetUSD     string `yaml:"budget_usd,omitempty" json:"budget_usd,omitempty"`
	TaskType      string `yaml:"task_type,omitempty" json:"task_type,omitempty"`
	OnBehalfOf    string `yaml:"on_behalf_of,omitempty" json:"on_behalf_of,omitempty"`
	Outcome       string `yaml:"outcome,omitempty" json:"outcome,omitempty"`
	ApprovalToken string `yaml:"approval_token,omitempty" json:"approval_token,omitempty"`
}

// Expect is the guardrail response a step should get back.
type Expect struct {
	// Status is the required HTTP status code, e.g. 402 (budget Breaker),
	// 403 (Wardryx deny/hold or a DLP block), or 200 (allowed).
	Status int `yaml:"status" json:"status"`
	// Header optionally matches response header values exactly, e.g.
	// {"x-fuse-wardryx": "deny"}. Header names are matched
	// case-insensitively, per HTTP.
	Header map[string]string `yaml:"header,omitempty" json:"header,omitempty"`
	// WithinRepeats asserts the guardrail fires by at most this many
	// attempts. Defaults to the step's Repeat (i.e. "by the last attempt")
	// when zero, negative, or greater than Repeat.
	WithinRepeats int `yaml:"within_repeats,omitempty" json:"within_repeats,omitempty"`
	// Event, when set, additionally asserts that a downstream, off-path
	// service (Verdryx, Idryx, Qryx, or any other agent-event emitter)
	// recorded a matching reaction within Event.Within, checked only after
	// the synchronous status/header assertion above has already matched.
	// See EventExpect and internal/watch.
	Event *EventExpect `yaml:"event,omitempty" json:"event,omitempty"`

	// Decision is a tool_check step's required assertion: DecisionAllow or
	// DecisionDeny, the "decision" the gateway's check-tool-call answered.
	Decision string `yaml:"decision,omitempty" json:"decision,omitempty"`
	// ErrorCode is an mcp step's assertion that the broker answered with a
	// JSON-RPC error carrying exactly this code (e.g. -32002, the broker's
	// raw-secret block). A pointer so that "not set" is not mistaken for 0.
	ErrorCode *int `yaml:"error_code,omitempty" json:"error_code,omitempty"`
	// ErrorContains optionally requires that error's message to contain this
	// text. It exists because an upstream MCP server may use the same number
	// for its own error (the MCP spec itself gives -32002 a meaning), so a
	// code alone could pass on an error the broker never raised.
	ErrorContains string `yaml:"error_contains,omitempty" json:"error_contains,omitempty"`
	// Result is the mcp step's other assertion: the broker answered with a
	// JSON-RPC result and no error. Exactly one of ErrorCode and Result.
	Result bool `yaml:"result,omitempty" json:"result,omitempty"`
}

// The two decisions a tool_check step can assert.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// EventExpect asserts that a downstream, off-path service's own
// agent-event log (agent-passport SPEC.md Sec 6) recorded a matching
// event, correlated by the run_id this step itself sent on the wire as
// x-fuse-run-id. This is how a scenario rehearses a guardrail whose
// effect is not visible on the gateway's own synchronous response -- e.g.
// Verdryx recording a quality_drift event, or Idryx's attestation_missing
// detector firing -- the same way Expect's Status/Header fields rehearse
// one that is (a Wardryx deny, a Breaker 402).
type EventExpect struct {
	// Source is the emitting product's agent-event "source" field, e.g.
	// "verdryx", "idryx", "qryx" (agent-passport SPEC.md Sec 6.1's source
	// registry).
	Source string `yaml:"source" json:"source"`
	// Type is the agent-event "type" field, e.g. "quality_drift",
	// "attestation_missing" (SPEC.md Sec 6.2's per-source type registry).
	Type string `yaml:"type" json:"type"`
	// Within bounds how long to wait for the event to appear after the
	// step's synchronous response matched. Defaults to DefaultEventWithin
	// when zero or negative.
	Within time.Duration `yaml:"within,omitempty" json:"within,omitempty"`
}

// ParseYAML decodes and validates one Scenario from YAML.
func ParseYAML(data []byte) (Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Scenario{}, fmt.Errorf("scenario: invalid yaml: %w", err)
	}
	if err := validate(&s); err != nil {
		return Scenario{}, err
	}
	return s, nil
}

// ParseJSON decodes and validates one Scenario from JSON.
func ParseJSON(data []byte) (Scenario, error) {
	var s Scenario
	if err := json.Unmarshal(data, &s); err != nil {
		return Scenario{}, fmt.Errorf("scenario: invalid json: %w", err)
	}
	if err := validate(&s); err != nil {
		return Scenario{}, err
	}
	return s, nil
}

// LoadFile reads and parses one scenario file, dispatching on its
// extension: .yaml/.yml for YAML, .json for JSON.
func LoadFile(path string) (Scenario, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-supplied CLI argument (a scenario file), not untrusted input
	if err != nil {
		return Scenario{}, fmt.Errorf("scenario: read %s: %w", path, err)
	}

	var s Scenario
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		s, err = ParseYAML(data)
	case ".json":
		s, err = ParseJSON(data)
	default:
		return Scenario{}, fmt.Errorf("scenario: %s: %w", path, ErrUnsupportedExt)
	}
	if err != nil {
		return Scenario{}, fmt.Errorf("scenario: %s: %w", path, err)
	}
	return s, nil
}

// LoadDir reads every .yaml/.yml/.json file directly under dir, not
// recursively, in sorted filename order, and returns the parsed and
// validated Scenarios.
//
// A malformed scenario file fails the whole load. This is deliberately
// stricter than the stack's telemetry connectors (internal/ingest/tokenfuse
// and friends in Idryx, agent-stack-go's own event.Scan), which tolerate a
// bad line rather than lose a whole batch: those read machine-generated
// logs, where partial data is normal. A scenario file is a hand-authored
// safety check, so silently dropping a malformed one would silently drop
// test coverage instead of surfacing the authoring mistake.
func LoadDir(dir string) ([]Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scenario: read dir %s: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml", ".json":
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	if len(names) == 0 {
		return nil, fmt.Errorf("%s: %w", dir, ErrNoScenarios)
	}

	out := make([]Scenario, 0, len(names))
	for _, name := range names {
		s, err := LoadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// validate checks required fields and normalizes defaults (Step.Repeat,
// Expect.WithinRepeats) in place, so every caller downstream of Parse/Load
// can trust Repeat >= 1 and 1 <= WithinRepeats <= Repeat without redoing
// this arithmetic itself.
func validate(s *Scenario) error {
	if s.Name == "" {
		return ErrMissingName
	}
	if len(s.Steps) == 0 {
		return fmt.Errorf("%s: %w", s.Name, ErrNoSteps)
	}
	for i := range s.Steps {
		step := &s.Steps[i]
		if step.Name == "" {
			return fmt.Errorf("%s: step %d: %w", s.Name, i, ErrStepMissingName)
		}
		if step.Repeat <= 0 {
			step.Repeat = 1
		}
		var err error
		switch step.Kind() {
		case KindMessages:
			err = validateMessagesStep(step)
		case KindToolCheck:
			err = validateToolCheckStep(s, step)
		case KindMCP:
			err = validateMCPStep(s, step)
		default:
			err = ErrStepKind
		}
		if err != nil {
			return fmt.Errorf("%s: step %s: %w", s.Name, step.Name, err)
		}
		if step.Expect.WithinRepeats <= 0 || step.Expect.WithinRepeats > step.Repeat {
			step.Expect.WithinRepeats = step.Repeat
		}
		if step.Expect.Event != nil {
			if step.Expect.Event.Source == "" {
				return fmt.Errorf("%s: step %s: %w", s.Name, step.Name, ErrEventMissingSource)
			}
			if step.Expect.Event.Type == "" {
				return fmt.Errorf("%s: step %s: %w", s.Name, step.Name, ErrEventMissingType)
			}
			if step.Expect.Event.Within <= 0 {
				step.Expect.Event.Within = DefaultEventWithin
			}
		}
	}
	return nil
}

// validateMessagesStep is the original step's validation, unchanged, plus the
// content-block rules and the refusal of expect fields that belong to the
// other two kinds.
func validateMessagesStep(step *Step) error {
	if step.Request.Model == "" {
		return ErrStepMissingModel
	}
	if len(step.Request.Messages) == 0 {
		return ErrStepMissingMessages
	}
	for _, m := range step.Request.Messages {
		for _, b := range m.Blocks {
			switch b.Type {
			case "text":
			case "tool_use":
				if b.ID == "" || b.Name == "" {
					return ErrBlockToolUse
				}
			case "tool_result":
				if b.ToolUseID == "" {
					return ErrBlockToolResult
				}
			default:
				return fmt.Errorf("%w: %q", ErrUnknownBlockType, b.Type)
			}
		}
	}
	if step.Expect.Status == 0 {
		return ErrStepMissingStatus
	}
	if step.Expect.Decision != "" || step.Expect.ErrorCode != nil ||
		step.Expect.ErrorContains != "" || step.Expect.Result {
		return fmt.Errorf("%w: decision, error_code, error_contains and result are for tool_check and mcp steps", ErrExpectKind)
	}
	return nil
}

func validateToolCheckStep(s *Scenario, step *Step) error {
	if step.ToolCheck.Tool == "" {
		return ErrToolCheckMissingTool
	}
	// An off firewall answers governed: false. That is the only thing that
	// lets this step read as "not configured" instead of as a gap, and it is
	// read through requires, so a scenario without it would report a plain
	// gateway as defective.
	if s.Requires != "taint" {
		return ErrToolCheckNeedsTaint
	}
	if step.Expect.Decision != DecisionAllow && step.Expect.Decision != DecisionDeny {
		return fmt.Errorf("%w: got %q", ErrToolCheckDecision, step.Expect.Decision)
	}
	if err := foreignExpect(step, "status and header are for messages steps"); err != nil {
		return err
	}
	if step.Expect.ErrorCode != nil || step.Expect.ErrorContains != "" || step.Expect.Result {
		return fmt.Errorf("%w: error_code, error_contains and result are for mcp steps", ErrExpectKind)
	}
	return unusedHeaders(step)
}

func validateMCPStep(s *Scenario, step *Step) error {
	if step.MCP.Method == "" {
		return ErrMCPMissingMethod
	}
	// Every broker guardrail is opt-in, so a scenario that names none could
	// never be read as "not configured".
	if s.Requires == "" {
		return ErrMCPNeedsRequires
	}
	if (step.Expect.ErrorCode != nil) == step.Expect.Result {
		return ErrMCPExpect
	}
	if step.Expect.ErrorContains != "" && step.Expect.ErrorCode == nil {
		return fmt.Errorf("%w: error_contains goes with error_code", ErrMCPExpect)
	}
	if step.Expect.Event != nil {
		return ErrMCPEvent
	}
	if err := foreignExpect(step, "status and header are for messages steps"); err != nil {
		return err
	}
	if step.Expect.Decision != "" {
		return fmt.Errorf("%w: decision is for tool_check steps", ErrExpectKind)
	}
	return unusedHeaders(step)
}

func foreignExpect(step *Step, why string) error {
	if step.Expect.Status != 0 || len(step.Expect.Header) > 0 {
		return fmt.Errorf("%w: %s", ErrExpectKind, why)
	}
	return nil
}

// unusedHeaders refuses headers a tool_check or mcp step never sends. A
// budget on an mcp step would read as a budget drill and enforce nothing.
func unusedHeaders(step *Step) error {
	h := step.Headers
	if h.BudgetUSD != "" || h.TaskType != "" || h.OnBehalfOf != "" || h.Outcome != "" || h.ApprovalToken != "" {
		return ErrHeadersUnused
	}
	return nil
}
