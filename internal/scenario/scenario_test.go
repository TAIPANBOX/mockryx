package scenario

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const validYAML = `
name: test-scenario
description: a test
requires: wardryx
steps:
  - name: step-one
    repeat: 3
    request:
      model: claude-haiku
      max_tokens: 50
      messages:
        - role: user
          content: hello
      tools:
        - name: shell_exec
          description: run a shell command
    headers:
      run_id: run-1
      agent_id: agent://test/bot
      budget_usd: "0.01"
      on_behalf_of: user://test/operator
    expect:
      status: 403
      header:
        x-fuse-wardryx: deny
      within_repeats: 2
`

func TestParseYAML(t *testing.T) {
	s, err := ParseYAML([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if s.Name != "test-scenario" || s.Requires != "wardryx" {
		t.Errorf("unexpected scenario: %+v", s)
	}
	if len(s.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(s.Steps))
	}

	step := s.Steps[0]
	if step.Request.Model != "claude-haiku" {
		t.Errorf("Model = %q", step.Request.Model)
	}
	if len(step.Request.Tools) != 1 || step.Request.Tools[0].Name != "shell_exec" {
		t.Errorf("Tools = %+v", step.Request.Tools)
	}
	if step.Headers.AgentID != "agent://test/bot" || step.Headers.OnBehalfOf != "user://test/operator" {
		t.Errorf("Headers = %+v", step.Headers)
	}
	if step.Expect.Status != 403 || step.Expect.Header["x-fuse-wardryx"] != "deny" {
		t.Errorf("Expect = %+v", step.Expect)
	}
	// within_repeats was set explicitly to 2, which is <= repeat (3), so
	// validate must leave it unchanged.
	if step.Expect.WithinRepeats != 2 {
		t.Errorf("WithinRepeats = %d, want 2", step.Expect.WithinRepeats)
	}
}

func TestParseJSON(t *testing.T) {
	data := `{
		"name": "test-scenario",
		"steps": [{
			"name": "step-one",
			"request": {"model": "claude-haiku", "messages": [{"role": "user", "content": "hi"}]},
			"expect": {"status": 402}
		}]
	}`
	s, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if s.Name != "test-scenario" {
		t.Errorf("Name = %q", s.Name)
	}
	if len(s.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(s.Steps))
	}
	if s.Steps[0].Repeat != 1 {
		t.Errorf("Repeat default = %d, want 1", s.Steps[0].Repeat)
	}
	if s.Steps[0].Expect.WithinRepeats != 1 {
		t.Errorf("WithinRepeats default = %d, want 1 (== repeat)", s.Steps[0].Expect.WithinRepeats)
	}
}

func TestParseJSONWithinRepeatsCappedAtRepeat(t *testing.T) {
	data := `{
		"name": "x",
		"steps": [{
			"name": "s",
			"repeat": 3,
			"request": {"model": "m", "messages": [{"role": "user", "content": "hi"}]},
			"expect": {"status": 402, "within_repeats": 99}
		}]
	}`
	s, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if s.Steps[0].Expect.WithinRepeats != 3 {
		t.Errorf("WithinRepeats = %d, want 3 (capped at repeat)", s.Steps[0].Expect.WithinRepeats)
	}
}

func TestParseYAMLInvalid(t *testing.T) {
	if _, err := ParseYAML([]byte("not: [valid yaml")); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

func TestParseJSONInvalid(t *testing.T) {
	if _, err := ParseJSON([]byte("{not valid json")); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestValidateMissingFields(t *testing.T) {
	const stepPrefix = `name: x
steps:
  - `

	cases := []struct {
		name string
		yaml string
		want error
	}{
		{
			"missing name",
			`steps:
  - name: s
    request: {model: m, messages: [{role: user, content: hi}]}
    expect: {status: 200}`,
			ErrMissingName,
		},
		{"no steps", `name: x`, ErrNoSteps},
		{
			"step missing name",
			stepPrefix + `request: {model: m, messages: [{role: user, content: hi}]}
    expect: {status: 200}`,
			ErrStepMissingName,
		},
		{
			"step missing model",
			stepPrefix + `name: s
    request: {messages: [{role: user, content: hi}]}
    expect: {status: 200}`,
			ErrStepMissingModel,
		},
		{
			"step missing messages",
			stepPrefix + `name: s
    request: {model: m}
    expect: {status: 200}`,
			ErrStepMissingMessages,
		},
		{
			"step missing status",
			stepPrefix + `name: s
    request: {model: m, messages: [{role: user, content: hi}]}
    expect: {}`,
			ErrStepMissingStatus,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseYAML([]byte(c.yaml))
			if !errors.Is(err, c.want) {
				t.Errorf("error = %v, want wrapping %v", err, c.want)
			}
		})
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b-scenario.json"),
		`{"name":"b","steps":[{"name":"s","request":{"model":"m","messages":[{"role":"user","content":"hi"}]},"expect":{"status":200}}]}`)
	writeFile(t, filepath.Join(dir, "a-scenario.yaml"), "name: a\n"+
		"steps:\n  - name: s\n    request:\n      model: m\n      messages:\n        - role: user\n          content: hi\n"+
		"    expect:\n      status: 200\n")
	writeFile(t, filepath.Join(dir, "ignore.txt"), "not a scenario")

	scenarios, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(scenarios) != 2 {
		t.Fatalf("got %d scenarios, want 2", len(scenarios))
	}
	// Sorted filename order: a-scenario.yaml before b-scenario.json.
	if scenarios[0].Name != "a" || scenarios[1].Name != "b" {
		t.Errorf("order = [%s, %s], want [a, b]", scenarios[0].Name, scenarios[1].Name)
	}
}

func TestLoadDirMalformedFailsWholeLoad(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "good.json"),
		`{"name":"good","steps":[{"name":"s","request":{"model":"m","messages":[{"role":"user","content":"hi"}]},"expect":{"status":200}}]}`)
	writeFile(t, filepath.Join(dir, "bad.json"), `{not valid json`)

	if _, err := LoadDir(dir); err == nil {
		t.Fatal("expected LoadDir to fail with a malformed scenario file present")
	}
}

func TestLoadDirEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadDir(dir); !errors.Is(err, ErrNoScenarios) {
		t.Errorf("error = %v, want wrapping ErrNoScenarios", err)
	}
}

func TestLoadDirMissing(t *testing.T) {
	if _, err := LoadDir(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestLoadFileUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scenario.txt")
	writeFile(t, path, "name: x")
	if _, err := LoadFile(path); !errors.Is(err, ErrUnsupportedExt) {
		t.Errorf("error = %v, want wrapping ErrUnsupportedExt", err)
	}
}

func TestLoadFileMissing(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// TestShippedExampleScenariosParse pins the example scenarios under
// scenarios/ to the parser: they must always be valid, loadable scenarios,
// so this fails loudly the moment they drift out of sync with the format.
func TestShippedExampleScenariosParse(t *testing.T) {
	scenarios, err := LoadDir("../../scenarios")
	if err != nil {
		t.Fatalf("LoadDir(../../scenarios): %v", err)
	}
	if len(scenarios) < 5 {
		t.Fatalf("got %d example scenarios, want at least 5", len(scenarios))
	}

	byName := map[string]Scenario{}
	for _, s := range scenarios {
		byName[s.Name] = s
	}
	for _, name := range []string{
		"runaway-budget", "wardryx-denied-tool", "dlp-secret-leak",
		"approval-required", "on-behalf-of-forged-chain",
	} {
		if _, ok := byName[name]; !ok {
			t.Errorf("expected shipped example scenario %q", name)
		}
	}
	if byName["wardryx-denied-tool"].Requires != "wardryx" {
		t.Error(`wardryx-denied-tool must declare requires: "wardryx"`)
	}
	if byName["dlp-secret-leak"].Requires != "dlp" {
		t.Error(`dlp-secret-leak must declare requires: "dlp"`)
	}
	if byName["runaway-budget"].Requires != "" {
		t.Error("runaway-budget rehearses a core guardrail and must not declare requires")
	}
	if byName["approval-required"].Requires != "wardryx" {
		t.Error(`approval-required must declare requires: "wardryx"`)
	}
	if byName["on-behalf-of-forged-chain"].Requires != "wardryx" {
		t.Error(`on-behalf-of-forged-chain must declare requires: "wardryx"`)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
