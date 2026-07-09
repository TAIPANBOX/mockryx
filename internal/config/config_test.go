package config

import "testing"

func TestFromEnv(t *testing.T) {
	t.Setenv("MOCKRYX_GATEWAY", "http://gw.local")
	t.Setenv("MOCKRYX_EVENTS_PATH", "/tmp/events.ndjson")
	t.Setenv("MOCKRYX_API_KEY", "secret-key")

	c := FromEnv()
	if c.Gateway != "http://gw.local" {
		t.Errorf("Gateway = %q", c.Gateway)
	}
	if c.EventsPath != "/tmp/events.ndjson" {
		t.Errorf("EventsPath = %q", c.EventsPath)
	}
	if c.APIKey != "secret-key" {
		t.Errorf("APIKey = %q", c.APIKey)
	}
}

func TestFromEnvEmpty(t *testing.T) {
	t.Setenv("MOCKRYX_GATEWAY", "")
	t.Setenv("MOCKRYX_EVENTS_PATH", "")
	t.Setenv("MOCKRYX_API_KEY", "")

	c := FromEnv()
	if c.Gateway != "" || c.EventsPath != "" || c.APIKey != "" {
		t.Errorf("expected an empty Config, got %+v", c)
	}
}
