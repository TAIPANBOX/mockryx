// Package config reads mockryx's environment-variable configuration.
package config

import "os"

// Config is mockryx's environment-derived configuration. It is read once,
// at startup; CLI flags, when provided, take precedence over these values
// (wired at the call site in cmd/mockryx).
type Config struct {
	// Gateway is the base URL of the TokenFuse gateway to rehearse
	// against, from MOCKRYX_GATEWAY.
	Gateway string
	// EventsPath is the NDJSON file mockryx appends its own sim_run,
	// sim_finding, and blast_radius_measured events to, from
	// MOCKRYX_EVENTS_PATH. Empty means events are disabled.
	EventsPath string
	// APIKey is the key mockryx sends as x-api-key on every crafted
	// request, from MOCKRYX_API_KEY. Empty means no key is sent.
	APIKey string
}

// FromEnv reads MOCKRYX_GATEWAY, MOCKRYX_EVENTS_PATH, and MOCKRYX_API_KEY.
func FromEnv() Config {
	return Config{
		Gateway:    os.Getenv("MOCKRYX_GATEWAY"),
		EventsPath: os.Getenv("MOCKRYX_EVENTS_PATH"),
		APIKey:     os.Getenv("MOCKRYX_API_KEY"),
	}
}
