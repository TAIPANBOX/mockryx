package scenario

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Holds invariant 6 of CLAUDE.md: drills target the operator's own gateway,
// always.
//
// The way that holds today is stronger than "no scenario hardcodes a host": the
// scenario FORMAT has no field a target could go in. The address comes from
// --gateway or $MOCKRYX_GATEWAY and from nowhere else, so a scenario file is
// physically incapable of pointing this tool at somebody else's system.
//
// That is a property worth defending directly, because the way it would be lost
// is a convenience: somebody adds `url:` or `base_url:` to a scenario "so the
// examples are self-contained", and mockryx quietly becomes a tool that carries
// its own targets around. This repo is a fire drill against infrastructure the
// operator owns; a scenario that names a target is a different product.
//
// Two checks, because either alone would be too weak. The first defends the
// format. The second defends the data, in case a field is ever added for a
// legitimate reason and then misused.

// targetShaped names a field that could carry somewhere to send a request.
//
// "broker" is here since the MCP broker steps arrived (2026-10-12): the
// broker's address comes from --mcp-broker or $MOCKRYX_MCP_BROKER and from
// nowhere else, which is the same property for the same reason.
var targetShaped = []string{"url", "host", "endpoint", "gateway", "addr", "address", "base", "target", "server", "upstream", "broker"}

func fieldLooksLikeTarget(name, tag string) bool {
	for _, candidate := range []string{strings.ToLower(name), strings.ToLower(tag)} {
		for _, w := range targetShaped {
			if strings.Contains(candidate, w) {
				return true
			}
		}
	}
	return false
}

func walkStruct(t *testing.T, typ reflect.Type, path string, seen map[reflect.Type]bool) {
	t.Helper()
	if seen[typ] {
		return
	}
	seen[typ] = true

	switch typ.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Array:
		walkStruct(t, typ.Elem(), path, seen)
		return
	case reflect.Map:
		walkStruct(t, typ.Elem(), path, seen)
		return
	case reflect.Struct:
	default:
		return
	}

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		where := path + "." + f.Name
		if tag != "" {
			where += " (yaml:" + tag + ")"
		}
		if fieldLooksLikeTarget(f.Name, tag) {
			t.Errorf(
				"the scenario format gained %s, a field that could carry a target. "+
					"Drills go where --gateway says and nowhere else: a scenario "+
					"that names its own target turns a fire drill against the "+
					"operator's own system into a tool that carries addresses "+
					"around. If this field is genuinely needed, it is a product "+
					"decision, not a convenience.",
				where,
			)
		}
		walkStruct(t, f.Type, where, seen)
	}
}

func TestScenarioFormatCannotCarryATarget(t *testing.T) {
	seen := map[reflect.Type]bool{}
	walkStruct(t, reflect.TypeOf(Scenario{}), "Scenario", seen)
	if len(seen) < 3 {
		t.Fatalf("only %d types were walked, so this test measured almost nothing", len(seen))
	}
	t.Logf("%d scenario types walked", len(seen))
}

// collectStrings appends every string reachable from v: struct fields, slice
// and array elements, map keys and values, and whatever an interface holds.
//
// It is reflection and not a hand-written list on purpose. The list this
// replaces named the fields that existed in August and silently went on
// naming only those: a URL placed in a tool_use input, a tool result, an mcp
// params object or a tool_check would have passed it. Walking the value
// means a field added tomorrow is inspected tomorrow, by construction.
func collectStrings(v reflect.Value, out *[]string) {
	switch v.Kind() {
	case reflect.String:
		*out = append(*out, v.String())
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			collectStrings(v.Elem(), out)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			collectStrings(v.Index(i), out)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			collectStrings(k, out)
			collectStrings(v.MapIndex(k), out)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				collectStrings(v.Field(i), out)
			}
		}
	}
}

func valueTexts(s Scenario) []string {
	var out []string
	collectStrings(reflect.ValueOf(s), &out)
	return out
}

// outboundEndpoint reports whether text names an http(s) endpoint that is not
// loopback.
func outboundEndpoint(text string) bool {
	low := strings.ToLower(text)
	if !strings.Contains(low, "http://") && !strings.Contains(low, "https://") {
		return false
	}
	return !strings.Contains(low, "127.0.0.1") && !strings.Contains(low, "localhost")
}

// TestEveryStringInAScenarioIsInspected plants an endpoint in every place a
// value can sit, including the ones added with tool history and the mcp and
// tool_check steps, and requires the inspection below to see each of them.
func TestEveryStringInAScenarioIsInspected(t *testing.T) {
	s, err := ParseYAML([]byte(`name: planted
description: "https://planted.example/description"
requires: taint
steps:
  - name: messages
    request:
      model: m
      messages:
        - role: user
          content: "https://planted.example/plain-content"
        - role: assistant
          content:
            - type: text
              text: "https://planted.example/block-text"
            - type: tool_use
              id: t1
              name: fetch_url
              input:
                nested: {deeper: ["https://planted.example/tool-input"]}
        - role: user
          content:
            - type: tool_result
              tool_use_id: t1
              content: "https://planted.example/tool-result"
    expect: {status: 200}
  - name: check
    tool_check: {tool: "https://planted.example/tool-name"}
    expect: {decision: deny}
  - name: broker
    mcp:
      method: tools/call
      params:
        arguments: {query: "https://planted.example/mcp-params"}
    expect: {result: true}
`))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	// The mcp step needs a requires of its own, which the scenario above has.
	seen := strings.Join(valueTexts(s), "\n")
	for _, marker := range []string{
		"planted.example/description", "planted.example/plain-content",
		"planted.example/block-text", "planted.example/tool-input",
		"planted.example/tool-result", "planted.example/tool-name",
		"planted.example/mcp-params",
	} {
		if !strings.Contains(seen, marker) {
			t.Errorf("the value inspection never sees %q, so a drill could carry a target there", marker)
		}
	}
}

// TestTheBrokerIsATargetShapedWord holds the format to the same rule for the
// broker as for the gateway: a field that could carry its address is refused.
func TestTheBrokerIsATargetShapedWord(t *testing.T) {
	for _, name := range []string{"Broker", "MCPBroker", "mcp_broker"} {
		if !fieldLooksLikeTarget(name, strings.ToLower(name)) {
			t.Errorf("a scenario field named %q would not be recognised as target-shaped", name)
		}
	}
}

// TestShippedScenariosNameNoTarget parses the shipped scenarios as YAML and
// looks for an outbound endpoint in any VALUE. Comments are excluded by
// construction, since a scenario's prose legitimately discusses the gateway.
//
// All three shipped directories are read: the broker drills in scenarios/mcp
// and the game-day drill are as able to carry a target as the top level is,
// and a plain LoadDir of scenarios/ never sees them.
func TestShippedScenariosNameNoTarget(t *testing.T) {
	inspected := 0
	for _, sub := range []string{"", "mcp", "game-day"} {
		dir := filepath.Join("..", "..", "scenarios", sub)
		scenarios, err := LoadDir(dir)
		if err != nil {
			t.Fatalf("cannot load scenarios in %s: %v", dir, err)
		}
		if len(scenarios) == 0 {
			t.Fatalf("no scenarios loaded from %s, so this test measured nothing", dir)
		}
		for _, s := range scenarios {
			for _, v := range valueTexts(s) {
				if outboundEndpoint(v) {
					t.Errorf("scenario %q carries an outbound endpoint in a value: %q", s.Name, v)
				}
			}
			inspected++
		}
	}
	t.Logf("%d scenarios inspected across scenarios/, scenarios/mcp and scenarios/game-day", inspected)
}
