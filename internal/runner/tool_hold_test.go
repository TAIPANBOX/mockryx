package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/mockryx/internal/scenario"
)

// The tool hold, on a model of the broker it reaches through: the scans first,
// then the policy gate, in the order and with the words TokenFuse v1.23.0 and
// wardryx v2.6.0 use. Each field is a state a real box can be in, so every drill
// in scenarios/mcp is shown reading right in each one.

const drillGrant = "agent://mockryx.local/"

// holdBroker answers tools/call the way the broker does.
type holdBroker struct {
	floor  bool // the policy carries when_no_grant: hold
	grants bool // and grant-drills beside it
	scans  bool // the DLP and PII scans block
	// Rules that stop the ungranted id before the floor is reached, and let the
	// drill identity through, so only the first step can tell them from the floor.
	denyUngranted bool // a deny rule
	otherHold     bool // a hold of another rule

	mu     sync.Mutex
	agents []string
}

func (b *holdBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		ID     int `json:"id"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	_ = json.Unmarshal(raw, &req)
	agent := r.Header.Get("x-fuse-agent-id")
	b.mu.Lock()
	b.agents = append(b.agents, agent)
	b.mu.Unlock()

	text := string(raw)
	reply := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, req.ID)
	refuse := func(code int, msg string) {
		enc, _ := json.Marshal(msg)
		reply = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":%d,"message":%s}}`, req.ID, code, enc)
	}
	switch {
	case b.scans && strings.Contains(text, "AKIA"):
		refuse(-32002, "blocked: raw secret in tool arguments (aws_access_key)")
	case b.scans && strings.Contains(text, "@example.com"):
		refuse(-32006, "blocked: pii in tool arguments (pii_email, pii_phone)")
	case b.denyUngranted && !strings.HasPrefix(agent, drillGrant):
		refuse(-32004, fmt.Sprintf("blocked: policy denied tool %q", req.Params.Name))
	case b.otherHold && !strings.HasPrefix(agent, drillGrant):
		refuse(-32004, fmt.Sprintf(`blocked: tool %q requires approval (approval ap_628813828092ec92155db7b26ace4363) (policy "slo-burn-hold" hold_if_signal: signal "slo.burn" is "exhausted"; human approval required)`, req.Params.Name))
	case b.floor && !(b.grants && strings.HasPrefix(agent, drillGrant)):
		refuse(-32004, fmt.Sprintf(`blocked: tool %q requires approval (approval ap_628813828092ec92155db7b26ace4363) (policy "tool-hold" when_no_grant: no policy grants tool %q to agent %q; human approval required)`,
			req.Params.Name, req.Params.Name, agent))
	}
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write([]byte(reply))
}

func runBrokerDrill(t *testing.T, file string, b *holdBroker) Result {
	t.Helper()
	s, err := scenario.LoadFile(filepath.Join("..", "..", "scenarios", "mcp", file))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	res := RunWithBroker(s, "http://127.0.0.1:1", srv.URL, "", nil)
	if len(b.agents) != len(s.Steps) {
		t.Fatalf("%s: the broker saw %d calls for %d steps, so the drill measured nothing", file, len(b.agents), len(s.Steps))
	}
	return res
}

const holdDrill = "mcp-ungranted-tool-held.yaml"

func TestToolHoldDrillPassesWhenOnlyTheUngrantedIdIsHeld(t *testing.T) {
	res := runBrokerDrill(t, holdDrill, &holdBroker{floor: true, grants: true})
	if res.Status != StatusPassed {
		t.Fatalf("status %s, findings %+v", res.Status, res.Findings)
	}
}

func TestToolHoldDrillFailsWhenTheDrillIdentityIsHeldToo(t *testing.T) {
	res := runBrokerDrill(t, holdDrill, &holdBroker{floor: true})
	if res.Status != StatusFailed || len(res.Findings) != 1 {
		t.Fatalf("status %s, findings %+v, want one Finding", res.Status, res.Findings)
	}
	f := res.Findings[0]
	if f.Step != "a-drill-identity-under-grant-drills-goes-through" || !strings.Contains(f.Got, "-32004") {
		t.Errorf("finding %+v, want the drill identity's step, refused -32004", f)
	}
}

func TestToolHoldDrillReadsAsNotConfiguredWhenTheHoldIsOff(t *testing.T) {
	res := runBrokerDrill(t, holdDrill, &holdBroker{})
	if res.Status != StatusSkipped || len(res.Findings) != 0 {
		t.Fatalf("status %s, findings %+v, want skipped_not_configured", res.Status, res.Findings)
	}
}

func TestToolHoldDrillFailsWhenAnotherGuardrailSpeaks(t *testing.T) {
	for name, b := range map[string]*holdBroker{
		"a deny":                 {denyUngranted: true},
		"a hold of another rule": {otherHold: true},
	} {
		t.Run(name, func(t *testing.T) {
			res := runBrokerDrill(t, holdDrill, b)
			if res.Status != StatusFailed {
				t.Fatalf("status %s, want failed: a -32004 that does not name when_no_grant is not this guardrail", res.Status)
			}
		})
	}
}

func TestAFindingQuotesTheWholeSentenceOfARefusal(t *testing.T) {
	long := strings.Repeat("why ", 60) + "the end"
	got := brokerMessage(long)
	if !strings.HasSuffix(got, "the end") {
		t.Errorf("a refusal of %d bytes was cut: %q", len(long), got)
	}
	if over := brokerMessage(strings.Repeat("x", 5000)); len(over) > 403 {
		t.Errorf("a reply of 5000 bytes came back as %d: a report has a limit", len(over))
	}
}

func TestScanDrillsReadAsTheyDidWithTheToolHoldOn(t *testing.T) {
	for _, file := range []string{"mcp-secret-in-arguments.yaml", "mcp-pii-in-arguments.yaml"} {
		t.Run(file, func(t *testing.T) {
			if res := runBrokerDrill(t, file, &holdBroker{floor: true, grants: true, scans: true}); res.Status != StatusPassed {
				t.Errorf("scans on: status %s, findings %+v", res.Status, res.Findings)
			}
			if res := runBrokerDrill(t, file, &holdBroker{floor: true, grants: true}); res.Status != StatusSkipped {
				t.Errorf("scans off: status %s, findings %+v, want skipped_not_configured", res.Status, res.Findings)
			}
		})
	}
}

func TestScanDrillsReportAFloorWithNoGrantsAsAFinding(t *testing.T) {
	for _, file := range []string{"mcp-secret-in-arguments.yaml", "mcp-pii-in-arguments.yaml"} {
		t.Run(file, func(t *testing.T) {
			res := runBrokerDrill(t, file, &holdBroker{floor: true})
			if res.Status != StatusFailed || len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Got, "when_no_grant") {
				t.Fatalf("status %s, findings %+v, want one Finding quoting when_no_grant", res.Status, res.Findings)
			}
		})
	}
}
