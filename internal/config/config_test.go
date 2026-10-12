package config

import "testing"

func TestFromEnv(t *testing.T) {
	t.Setenv("MOCKRYX_GATEWAY", "http://gw.local")
	t.Setenv("MOCKRYX_EVENTS_PATH", "/tmp/events.ndjson")
	t.Setenv("MOCKRYX_API_KEY", "secret-key")
	t.Setenv("MOCKRYX_WATCH_EVENTS", "/tmp/verdryx.ndjson, /tmp/idryx.ndjson")
	t.Setenv("MOCKRYX_MCP_BROKER", "http://broker.local:4200")

	c := FromEnv()
	if c.MCPBroker != "http://broker.local:4200" {
		t.Errorf("MCPBroker = %q", c.MCPBroker)
	}
	if c.Gateway != "http://gw.local" {
		t.Errorf("Gateway = %q", c.Gateway)
	}
	if c.EventsPath != "/tmp/events.ndjson" {
		t.Errorf("EventsPath = %q", c.EventsPath)
	}
	if c.APIKey != "secret-key" {
		t.Errorf("APIKey = %q", c.APIKey)
	}
	wantPaths := []string{"/tmp/verdryx.ndjson", "/tmp/idryx.ndjson"}
	if len(c.WatchEvents) != len(wantPaths) || c.WatchEvents[0] != wantPaths[0] || c.WatchEvents[1] != wantPaths[1] {
		t.Errorf("WatchEvents = %v, want %v (comma-split, whitespace-trimmed)", c.WatchEvents, wantPaths)
	}
}

func TestFromEnvEmpty(t *testing.T) {
	t.Setenv("MOCKRYX_GATEWAY", "")
	t.Setenv("MOCKRYX_EVENTS_PATH", "")
	t.Setenv("MOCKRYX_API_KEY", "")
	t.Setenv("MOCKRYX_WATCH_EVENTS", "")
	t.Setenv("MOCKRYX_MCP_BROKER", "")

	c := FromEnv()
	if c.Gateway != "" || c.EventsPath != "" || c.APIKey != "" || c.WatchEvents != nil || c.MCPBroker != "" {
		t.Errorf("expected an empty Config, got %+v", c)
	}
}
