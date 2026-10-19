package runner

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/TAIPANBOX/mockryx/internal/scenario"
)

const testAPIKey = "mockryx-fixture-api-key-0002"

// TestFindingsNeverCarryTheAPIKey: a gateway that echoes the x-api-key back, in a
// header and in its body, must not put the key into any Finding the run keeps,
// the same guarantee the client key has.
func TestFindingsNeverCarryTheAPIKey(t *testing.T) {
	rec := &recorder{}
	echo := func(req *http.Request) (int, map[string]string, string) {
		k := req.Header.Get("x-api-key")
		return http.StatusInternalServerError, map[string]string{"x-echo": k}, "not json " + k
	}
	srv := stubAll(t, rec, echo, echo, answerRPCResult)
	s := scenario.Scenario{
		Name:  "echo-api-key",
		Steps: []scenario.Step{messagesStep("m", "agent://test/bot")},
	}

	res := RunWithIdentity(s, srv.URL, "", testAPIKey, Identity{}, nil)

	if len(res.Findings) == 0 {
		t.Fatal("the scenario produced no findings, so this measured nothing")
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), testAPIKey) {
		t.Errorf("the serialised result contains the x-api-key: %s", out)
	}
	if !strings.Contains(string(out), redactMark) {
		t.Errorf("the echo was never redacted, so this measured nothing: %s", out)
	}
}

// TestRedactFindingSkipsAnEmptyKeyAndKeepsTheRest: with no key configured, a
// Finding's text must come through unchanged. Replacing the empty string would
// mark every position in it.
func TestRedactFindingSkipsAnEmptyKeyAndKeepsTheRest(t *testing.T) {
	f := Finding{Detail: "expected status 200", Got: "got 500", GotHeaders: map[string]string{"X-Echo": "value"}}
	redactFinding(&f, "", "")
	if f.Detail != "expected status 200" || f.Got != "got 500" || f.GotHeaders["X-Echo"] != "value" {
		t.Errorf("an empty key changed a finding: %+v", f)
	}
}
