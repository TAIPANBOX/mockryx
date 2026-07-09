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
	ErrUnsupportedExt      = errors.New("scenario: unsupported file extension (want .yaml, .yml, or .json)")
	ErrNoScenarios         = errors.New("scenario: no .yaml, .yml, or .json scenario files found")
)

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
	// When set, the runner also watches every response for the
	// "x-fuse-<requires>" header family (lowercased). If that header never
	// once appears across every attempt, the gateway plainly does not have
	// the feature wired in, and the runner reports "guardrail not
	// configured" instead of a false failure.
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
	// Request is the crafted call sent to POST {gateway}/v1/messages.
	Request Request `yaml:"request" json:"request"`
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
// Messages API's {role, content} shape.
type Message struct {
	Role    string `yaml:"role" json:"role"`
	Content string `yaml:"content" json:"content"`
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
		if step.Request.Model == "" {
			return fmt.Errorf("%s: step %s: %w", s.Name, step.Name, ErrStepMissingModel)
		}
		if len(step.Request.Messages) == 0 {
			return fmt.Errorf("%s: step %s: %w", s.Name, step.Name, ErrStepMissingMessages)
		}
		if step.Expect.Status == 0 {
			return fmt.Errorf("%s: step %s: %w", s.Name, step.Name, ErrStepMissingStatus)
		}
		if step.Expect.WithinRepeats <= 0 || step.Expect.WithinRepeats > step.Repeat {
			step.Expect.WithinRepeats = step.Repeat
		}
	}
	return nil
}
