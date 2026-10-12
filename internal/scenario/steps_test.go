package scenario

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Behaviours added by the 2026-10-12 decision (CLAUDE.md, "Escalate"): a
// step's request may carry structured content blocks (tool history), and two
// new step kinds exist, tool_check and mcp. Each test below names one
// behaviour. Every one was run red first; see VALIDATION.md.

// A conversation as an agent loop resends it: the user asks, the assistant
// invoked a tool, the tool's output came back as a tool_result.
const historyYAML = `
name: history
steps:
  - name: after-a-fetch
    request:
      model: claude-haiku
      max_tokens: 50
      messages:
        - role: user
          content: "Summarise the vendor page."
        - role: assistant
          content:
            - type: text
              text: "Fetching it."
            - type: tool_use
              id: toolu_mockryx_01
              name: fetch_url
              input:
                page: vendor-pricing
                depth: 1
        - role: user
          content:
            - type: tool_result
              tool_use_id: toolu_mockryx_01
              content: "Vendor pricing, three tiers."
      tools:
        - name: fetch_url
    expect:
      status: 200
`

func TestMessageContentBlocksParseFromYAML(t *testing.T) {
	s, err := ParseYAML([]byte(historyYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	msgs := s.Steps[0].Request.Messages
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	if msgs[0].Content != "Summarise the vendor page." || len(msgs[0].Blocks) != 0 {
		t.Errorf("a plain string message must stay a plain string: %+v", msgs[0])
	}
	if msgs[1].Content != "" || len(msgs[1].Blocks) != 2 {
		t.Fatalf("assistant message = %+v, want two blocks and no plain content", msgs[1])
	}
	use := msgs[1].Blocks[1]
	if use.Type != "tool_use" || use.ID != "toolu_mockryx_01" || use.Name != "fetch_url" {
		t.Errorf("tool_use block = %+v", use)
	}
	if use.Input["page"] != "vendor-pricing" || use.Input["depth"] != 1 {
		t.Errorf("tool_use input = %#v", use.Input)
	}
	res := msgs[2].Blocks[0]
	if res.Type != "tool_result" || res.ToolUseID != "toolu_mockryx_01" || res.Content != "Vendor pricing, three tiers." {
		t.Errorf("tool_result block = %+v", res)
	}
}

func TestMessageContentBlocksParseFromJSON(t *testing.T) {
	data := `{
		"name": "history",
		"steps": [{
			"name": "after-a-fetch",
			"request": {"model": "claude-haiku", "messages": [
				{"role": "user", "content": "go"},
				{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "fetch_url", "input": {"page": "p"}}]},
				{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "text"}]}
			]},
			"expect": {"status": 200}
		}]
	}`
	s, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	msgs := s.Steps[0].Request.Messages
	if msgs[0].Content != "go" {
		t.Errorf("plain string message = %+v", msgs[0])
	}
	if len(msgs[1].Blocks) != 1 || msgs[1].Blocks[0].Name != "fetch_url" || msgs[1].Blocks[0].Input["page"] != "p" {
		t.Errorf("tool_use block = %+v", msgs[1].Blocks)
	}
	if len(msgs[2].Blocks) != 1 || msgs[2].Blocks[0].ToolUseID != "t1" {
		t.Errorf("tool_result block = %+v", msgs[2].Blocks)
	}
}

// Additive: a scenario written before blocks existed marshals onto the wire
// byte for byte as it did, so no operator's drill changes under them.
func TestPlainStringContentMarshalsExactlyAsBefore(t *testing.T) {
	req := Request{
		Model:     "claude-haiku",
		MaxTokens: 50,
		Messages:  []Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: ""}},
		Tools:     []Tool{{Name: "shell_exec", Description: "run"}},
	}
	got, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"model":"claude-haiku","max_tokens":50,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":""}],"tools":[{"name":"shell_exec","description":"run"}]}`
	if string(got) != want {
		t.Errorf("wire body changed for a plain-string request:\n got  %s\n want %s", got, want)
	}
}

func TestBlocksMarshalTheAnthropicWireShape(t *testing.T) {
	m := Message{Role: "assistant", Blocks: []Block{
		{Type: "text", Text: "Fetching it."},
		{Type: "tool_use", ID: "t1", Name: "fetch_url"}, // no input given
		{Type: "tool_result", ToolUseID: "t1", Content: "text"},
	}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Role    string           `json:"role"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not the wire shape: %v: %s", err, raw)
	}
	if got.Role != "assistant" || len(got.Content) != 3 {
		t.Fatalf("got %s", raw)
	}
	if got.Content[0]["type"] != "text" || got.Content[0]["text"] != "Fetching it." {
		t.Errorf("text block = %v", got.Content[0])
	}
	use := got.Content[1]
	if use["type"] != "tool_use" || use["id"] != "t1" || use["name"] != "fetch_url" {
		t.Errorf("tool_use block = %v", use)
	}
	// The wire requires `input` on a tool_use even when the tool takes none.
	in, present := use["input"].(map[string]any)
	if !present || len(in) != 0 {
		t.Errorf("a tool_use with no input must still carry input: {} on the wire, got %v", use)
	}
	res := got.Content[2]
	if res["type"] != "tool_result" || res["tool_use_id"] != "t1" || res["content"] != "text" {
		t.Errorf("tool_result block = %v", res)
	}
}

func parseErr(t *testing.T, y string) error {
	t.Helper()
	_, err := ParseYAML([]byte(y))
	return err
}

func TestUnknownContentBlockTypeIsRefused(t *testing.T) {
	err := parseErr(t, `name: x
steps:
  - name: s
    request:
      model: m
      messages:
        - role: user
          content:
            - type: image
              text: nope
    expect: {status: 200}`)
	if !errors.Is(err, ErrUnknownBlockType) {
		t.Errorf("error = %v, want wrapping ErrUnknownBlockType", err)
	}
}

func TestToolUseBlockNeedsAnIDAndAName(t *testing.T) {
	for name, block := range map[string]string{
		"no id":   `{type: tool_use, name: fetch_url}`,
		"no name": `{type: tool_use, id: t1}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := parseErr(t, `name: x
steps:
  - name: s
    request:
      model: m
      messages:
        - role: assistant
          content: [`+block+`]
    expect: {status: 200}`)
			if !errors.Is(err, ErrBlockToolUse) {
				t.Errorf("error = %v, want wrapping ErrBlockToolUse", err)
			}
		})
	}
}

func TestToolResultBlockNeedsAToolUseID(t *testing.T) {
	err := parseErr(t, `name: x
steps:
  - name: s
    request:
      model: m
      messages:
        - role: user
          content: [{type: tool_result, content: text}]
    expect: {status: 200}`)
	if !errors.Is(err, ErrBlockToolResult) {
		t.Errorf("error = %v, want wrapping ErrBlockToolResult", err)
	}
}

// --- tool_check -------------------------------------------------------

const toolCheckYAML = `
name: egress
requires: taint
steps:
  - name: clean-run-may-post
    tool_check:
      tool: http_post
    headers:
      agent_id: agent://mockryx.local/rehearsal/researcher
    expect:
      decision: allow
`

func TestParseToolCheckStep(t *testing.T) {
	s, err := ParseYAML([]byte(toolCheckYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	st := s.Steps[0]
	if st.Kind() != KindToolCheck {
		t.Errorf("Kind = %q, want %q", st.Kind(), KindToolCheck)
	}
	if st.ToolCheck == nil || st.ToolCheck.Tool != "http_post" {
		t.Errorf("ToolCheck = %+v", st.ToolCheck)
	}
	if st.Expect.Decision != "allow" {
		t.Errorf("Expect.Decision = %q", st.Expect.Decision)
	}
	if st.Repeat != 1 || st.Expect.WithinRepeats != 1 {
		t.Errorf("defaults: repeat=%d within=%d, want 1 and 1", st.Repeat, st.Expect.WithinRepeats)
	}
}

func TestToolCheckStepNeedsRequiresTaint(t *testing.T) {
	// An off firewall answers governed:false. Without requires: taint there
	// is nothing to read that as "not configured", and a default-off guardrail
	// would be reported as a gap on every plain gateway.
	for name, requires := range map[string]string{"none": "", "other": "requires: dlp\n"} {
		t.Run(name, func(t *testing.T) {
			y := strings.Replace(toolCheckYAML, "requires: taint\n", requires, 1)
			if err := parseErr(t, y); !errors.Is(err, ErrToolCheckNeedsTaint) {
				t.Errorf("error = %v, want wrapping ErrToolCheckNeedsTaint", err)
			}
		})
	}
}

func TestToolCheckStepNeedsAToolAndAKnownDecision(t *testing.T) {
	noTool := strings.Replace(toolCheckYAML, "tool: http_post", "tool: \"\"", 1)
	if err := parseErr(t, noTool); !errors.Is(err, ErrToolCheckMissingTool) {
		t.Errorf("no tool: error = %v, want wrapping ErrToolCheckMissingTool", err)
	}
	for _, d := range []string{"", "maybe"} {
		y := strings.Replace(toolCheckYAML, "decision: allow", "decision: \""+d+"\"", 1)
		if err := parseErr(t, y); !errors.Is(err, ErrToolCheckDecision) {
			t.Errorf("decision %q: error = %v, want wrapping ErrToolCheckDecision", d, err)
		}
	}
}

func TestToolCheckStepMayAskForADownstreamEvent(t *testing.T) {
	y := toolCheckYAML + "      event: {source: tokenfuse, type: taint_block}\n"
	s, err := ParseYAML([]byte(y))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	ev := s.Steps[0].Expect.Event
	if ev == nil || ev.Source != "tokenfuse" || ev.Type != "taint_block" || ev.Within != DefaultEventWithin {
		t.Errorf("Expect.Event = %+v", ev)
	}
}

// --- mcp --------------------------------------------------------------

const mcpYAML = `
name: broker-secret
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

func TestParseMCPStep(t *testing.T) {
	s, err := ParseYAML([]byte(mcpYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	st := s.Steps[0]
	if st.Kind() != KindMCP {
		t.Errorf("Kind = %q, want %q", st.Kind(), KindMCP)
	}
	if st.MCP == nil || st.MCP.Method != "tools/call" || st.MCP.Params["name"] != "search_notes" {
		t.Fatalf("MCP = %+v", st.MCP)
	}
	args, _ := st.MCP.Params["arguments"].(map[string]any)
	if args["query"] != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("params.arguments = %#v", st.MCP.Params["arguments"])
	}
	if st.Expect.ErrorCode == nil || *st.Expect.ErrorCode != -32002 || st.Expect.ErrorContains != "blocked" {
		t.Errorf("Expect = %+v", st.Expect)
	}
}

func TestMCPStepMayExpectAResultInstead(t *testing.T) {
	y := strings.Replace(mcpYAML, "      error_code: -32002\n      error_contains: blocked\n", "      result: true\n", 1)
	s, err := ParseYAML([]byte(y))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if !s.Steps[0].Expect.Result || s.Steps[0].Expect.ErrorCode != nil {
		t.Errorf("Expect = %+v", s.Steps[0].Expect)
	}
}

func TestMCPStepNeedsAMethodAndARequires(t *testing.T) {
	noMethod := strings.Replace(mcpYAML, "method: tools/call", "method: \"\"", 1)
	if err := parseErr(t, noMethod); !errors.Is(err, ErrMCPMissingMethod) {
		t.Errorf("no method: error = %v, want wrapping ErrMCPMissingMethod", err)
	}
	// A broker guardrail is always optional: a scenario that names none could
	// never be read as "not configured", so it is refused rather than shipped.
	noRequires := strings.Replace(mcpYAML, "requires: mcp-dlp\n", "", 1)
	if err := parseErr(t, noRequires); !errors.Is(err, ErrMCPNeedsRequires) {
		t.Errorf("no requires: error = %v, want wrapping ErrMCPNeedsRequires", err)
	}
}

func TestMCPStepExpectsExactlyOneOutcome(t *testing.T) {
	neither := strings.Replace(mcpYAML, "      error_code: -32002\n      error_contains: blocked\n", "      error_contains: blocked\n", 1)
	if err := parseErr(t, neither); !errors.Is(err, ErrMCPExpect) {
		t.Errorf("neither: error = %v, want wrapping ErrMCPExpect", err)
	}
	both := strings.Replace(mcpYAML, "      error_code: -32002\n", "      error_code: -32002\n      result: true\n", 1)
	if err := parseErr(t, both); !errors.Is(err, ErrMCPExpect) {
		t.Errorf("both: error = %v, want wrapping ErrMCPExpect", err)
	}
}

func TestMCPStepCannotExpectADownstreamEvent(t *testing.T) {
	// The broker's own events carry no run_id, so a watcher could not
	// correlate one to this step. Refused rather than left to time out.
	y := mcpYAML + "      event: {source: tokenfuse, type: tool_call}\n"
	if err := parseErr(t, y); !errors.Is(err, ErrMCPEvent) {
		t.Errorf("error = %v, want wrapping ErrMCPEvent", err)
	}
}

// --- shape rules shared by the kinds ------------------------------------

func TestAStepMustSetExactlyOneKind(t *testing.T) {
	none := `name: x
steps:
  - name: s
    expect: {status: 200}`
	if err := parseErr(t, none); !errors.Is(err, ErrStepKind) {
		t.Errorf("none: error = %v, want wrapping ErrStepKind", err)
	}
	two := `name: x
requires: taint
steps:
  - name: s
    request: {model: m, messages: [{role: user, content: hi}]}
    tool_check: {tool: http_post}
    expect: {status: 200}`
	if err := parseErr(t, two); !errors.Is(err, ErrStepKind) {
		t.Errorf("two: error = %v, want wrapping ErrStepKind", err)
	}
}

func TestStepKindOfAnOrdinaryStepIsMessages(t *testing.T) {
	s, err := ParseYAML([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if s.Steps[0].Kind() != KindMessages {
		t.Errorf("Kind = %q, want %q", s.Steps[0].Kind(), KindMessages)
	}
}

func TestExpectFieldsOfAnotherKindAreRefused(t *testing.T) {
	cases := map[string]string{
		"status on a tool_check": strings.Replace(toolCheckYAML, "decision: allow", "decision: allow\n      status: 200", 1),
		"decision on a messages step": `name: x
steps:
  - name: s
    request: {model: m, messages: [{role: user, content: hi}]}
    expect: {status: 200, decision: deny}`,
		"error_code on a messages step": `name: x
steps:
  - name: s
    request: {model: m, messages: [{role: user, content: hi}]}
    expect: {status: 200, error_code: -32002}`,
		"header on an mcp step": strings.Replace(mcpYAML, "      error_contains: blocked\n", "      error_contains: blocked\n      header: {x-fuse-dlp: blocked}\n", 1),
	}
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			if err := parseErr(t, y); !errors.Is(err, ErrExpectKind) {
				t.Errorf("error = %v, want wrapping ErrExpectKind", err)
			}
		})
	}
}

func TestNewStepKindsRefuseHeadersTheyDoNotSend(t *testing.T) {
	// Only run_id and agent_id travel with a tool_check or an mcp call. A
	// budget on an mcp step would read as a budget drill and enforce nothing.
	for name, y := range map[string]string{
		"tool_check": strings.Replace(toolCheckYAML, "    headers:\n", "    headers:\n      budget_usd: \"1.00\"\n", 1),
		"mcp":        strings.Replace(mcpYAML, "    headers:\n", "    headers:\n      on_behalf_of: user://x/y\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := parseErr(t, y); !errors.Is(err, ErrHeadersUnused) {
				t.Errorf("error = %v, want wrapping ErrHeadersUnused", err)
			}
		})
	}
}
