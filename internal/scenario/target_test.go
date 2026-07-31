package scenario

import (
	"os"
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
var targetShaped = []string{"url", "host", "endpoint", "gateway", "addr", "address", "base", "target", "server", "upstream"}

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

// TestShippedScenariosNameNoTarget parses the shipped scenarios as YAML and
// looks for an outbound endpoint in any VALUE. Comments are excluded by
// construction, since a scenario's prose legitimately discusses the gateway.
func TestShippedScenariosNameNoTarget(t *testing.T) {
	dir := filepath.Join("..", "..", "scenarios")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", dir, err)
	}

	scenarios, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("cannot load scenarios: %v", err)
	}
	if len(scenarios) == 0 {
		t.Fatal("no scenarios loaded, so this test measured nothing")
	}

	// Re-serialise what was parsed, so only values are inspected.
	for _, s := range scenarios {
		var texts []string
		texts = append(texts, s.Name, s.Description, s.Requires)
		for _, step := range s.Steps {
			texts = append(texts, step.Name, step.Request.Model)
			for _, m := range step.Request.Messages {
				texts = append(texts, m.Role, m.Content)
			}
			for _, tool := range step.Request.Tools {
				texts = append(texts, tool.Name, tool.Description)
			}
			texts = append(texts, step.Headers.RunID, step.Headers.AgentID,
				step.Headers.TaskType, step.Headers.OnBehalfOf)
		}
		for _, v := range texts {
			low := strings.ToLower(v)
			if strings.Contains(low, "http://") || strings.Contains(low, "https://") {
				if strings.Contains(low, "127.0.0.1") || strings.Contains(low, "localhost") {
					continue
				}
				t.Errorf("scenario %q carries an outbound endpoint in a value: %q", s.Name, v)
			}
		}
	}
	t.Logf("%d scenarios and %d files inspected", len(scenarios), len(entries))
}
