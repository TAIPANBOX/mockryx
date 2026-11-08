package runner

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/TAIPANBOX/mockryx/internal/scenario"
)

// testBrokerKey is a fixture, not a credential: it exists only in these tests,
// and differs from testClientKey on purpose.
const testBrokerKey = "mockryx-fixture-broker-key-0003"

// allThreeKinds is one scenario with a messages step, a tool check and an mcp
// step, so one run sends to the gateway on two routes and to the broker.
func allThreeKinds() scenario.Scenario {
	return scenario.Scenario{
		Name:     "three-kinds",
		Requires: "taint",
		Steps: []scenario.Step{
			messagesStep("m", "agent://test/bot"),
			toolCheckStep("t", "agent://test/bot"),
			mcpStep("b", "agent://test/bot"),
		},
	}
}

// TestMCPStepSendsTheBrokerKeyAsXFuseKey: the broker has a door of its own, and a
// broker key given to the run is what that door is shown.
func TestMCPStepSendsTheBrokerKeyAsXFuseKey(t *testing.T) {
	rec := &recorder{}
	srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)
	s := scenario.Scenario{Name: "broker", Requires: "mcp-dlp", Steps: []scenario.Step{mcpStep("b", "agent://test/bot")}}

	res := RunWithIdentity(s, srv.URL, srv.URL+"/broker", "", Identity{BrokerKey: testBrokerKey}, nil)

	if res.Status != StatusPassed {
		t.Fatalf("status = %s, want passed so the broker was reached", res.Status)
	}
	got := rec.requests()
	if len(got) != 1 || got[0].path != "/broker" || got[0].key != testBrokerKey {
		t.Fatalf("broker saw %+v, want one request carrying the broker key in x-fuse-key", got)
	}
}

// TestBrokerKeyGoesToTheBrokerOnly: with both keys set, each service sees its own
// key and never the other's. The gateway's two routes get the client key, the
// broker gets the broker key.
func TestBrokerKeyGoesToTheBrokerOnly(t *testing.T) {
	rec := &recorder{}
	srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)

	res := RunWithIdentity(allThreeKinds(), srv.URL, srv.URL+"/broker", "",
		Identity{ClientKey: testClientKey, BrokerKey: testBrokerKey}, nil)

	got := rec.requests()
	if len(got) != 3 {
		t.Fatalf("saw %d requests (%s), want 3, so this measured nothing", len(got), res.Status)
	}
	for _, r := range got {
		want := testClientKey
		if r.path == "/broker" {
			want = testBrokerKey
		}
		if r.key != want {
			t.Errorf("request to %s carried x-fuse-key %q, want %q", r.path, r.key, want)
		}
	}
}

// TestClientKeyStillNeverReachesTheBrokerWithABrokerKeySet is invariant 13 held
// across the new setting: a broker key does not open a door for the client key,
// and with no broker key the broker is still sent nothing.
func TestClientKeyStillNeverReachesTheBrokerWithABrokerKeySet(t *testing.T) {
	for name, id := range map[string]Identity{
		"client key only":      {ClientKey: testClientKey},
		"client and broker":    {ClientKey: testClientKey, BrokerKey: testBrokerKey},
		"broker key only":      {BrokerKey: testBrokerKey},
		"neither key (as was)": {},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)
			RunWithIdentity(allThreeKinds(), srv.URL, srv.URL+"/broker", "", id, nil)

			sawBroker := false
			for _, r := range rec.requests() {
				if r.path != "/broker" {
					continue
				}
				sawBroker = true
				if r.key == testClientKey {
					t.Errorf("the broker received the gateway client key")
				}
				if r.key != id.BrokerKey {
					t.Errorf("the broker received x-fuse-key %q, want exactly the broker key %q", r.key, id.BrokerKey)
				}
			}
			if !sawBroker {
				t.Fatal("the broker was never reached, so this measured nothing")
			}
		})
	}
}

// TestBrokerKeyNeverReachesTheGateway: a run with only a broker key sends the
// gateway no x-fuse-key at all.
func TestBrokerKeyNeverReachesTheGateway(t *testing.T) {
	rec := &recorder{}
	srv := stubAll(t, rec, answerOK, answerAllow, answerRPCResult)

	RunWithIdentity(allThreeKinds(), srv.URL, srv.URL+"/broker", "", Identity{BrokerKey: testBrokerKey}, nil)

	gateway := 0
	for _, r := range rec.requests() {
		if r.path == "/broker" {
			continue
		}
		gateway++
		if r.key != "" {
			t.Errorf("the gateway route %s received x-fuse-key %q; the broker key must never leave for the gateway", r.path, r.key)
		}
	}
	if gateway != 2 {
		t.Fatalf("%d gateway requests, want 2, so this measured nothing", gateway)
	}
}

// TestFindingsNeverCarryTheBrokerKey: a broker that echoes the key back, in a
// header, in a non-protocol body and in an error message, must not put it into
// any Finding the run keeps.
func TestFindingsNeverCarryTheBrokerKey(t *testing.T) {
	for name, reply := range map[string]func(*http.Request) (int, map[string]string, string){
		"not json": func(req *http.Request) (int, map[string]string, string) {
			k := req.Header.Get("x-fuse-key")
			return http.StatusUnauthorized, map[string]string{"x-echo": k}, "refused " + k
		},
		"rpc error": func(req *http.Request) (int, map[string]string, string) {
			k := req.Header.Get("x-fuse-key")
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32004, "message": "blocked: " + k}})
			return http.StatusOK, map[string]string{"x-echo": k}, string(b)
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			srv := stubAll(t, rec, answerOK, answerAllow, reply)
			s := scenario.Scenario{Name: "echo", Steps: []scenario.Step{mcpStep("b", "agent://test/bot")}}

			res := RunWithIdentity(s, srv.URL, srv.URL+"/broker", "", Identity{BrokerKey: testBrokerKey}, nil)

			if len(res.Findings) == 0 {
				t.Fatal("the scenario produced no findings, so this measured nothing")
			}
			out, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), testBrokerKey) {
				t.Errorf("the serialised result contains the broker key: %s", out)
			}
			if !strings.Contains(string(out), redactMark) {
				t.Errorf("the echo was never redacted, so this measured nothing: %s", out)
			}
		})
	}
}
