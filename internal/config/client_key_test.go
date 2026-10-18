package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFromEnvReadsTheClientKeyAndAgentIDVariables(t *testing.T) {
	t.Setenv("MOCKRYX_CLIENT_KEY", "value-from-env")
	t.Setenv("MOCKRYX_CLIENT_KEY_FILE", "/run/secrets/mockryx-key")
	t.Setenv("MOCKRYX_AGENT_ID", "agent://td/agents/mockryx")

	c := FromEnv()

	if c.ClientKey != "value-from-env" || c.ClientKeyFile != "/run/secrets/mockryx-key" || c.AgentID != "agent://td/agents/mockryx" {
		t.Fatalf("FromEnv = %+v, want the three variables read", c)
	}
}

func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "client-key")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadClientKeyFileReturnsTheKeyWithoutTheTrailingNewline(t *testing.T) {
	got, err := ReadClientKeyFile(writeKeyFile(t, "abcDEF123\n"))
	if err != nil {
		t.Fatalf("ReadClientKeyFile: %v", err)
	}
	if got != "abcDEF123" {
		t.Errorf("key = %q, want the key without its newline", got)
	}
}

func TestReadClientKeyFileRefusesAnEmptyFile(t *testing.T) {
	for _, content := range []string{"", "\n", "  \r\n"} {
		if _, err := ReadClientKeyFile(writeKeyFile(t, content)); err == nil {
			t.Errorf("content %q: want a refusal, got none", content)
		}
	}
}

func TestReadClientKeyFileRefusesAFileThatIsNotOneKey(t *testing.T) {
	const secret = "keyPartOneXYZ keyPartTwoXYZ"
	_, err := ReadClientKeyFile(writeKeyFile(t, secret+"\n"))
	if err == nil {
		t.Fatal("two words in a key file: want a refusal, got none")
	}
	if strings.Contains(err.Error(), "keyPartOneXYZ") {
		t.Errorf("error %q repeats the file's contents", err)
	}
}

func TestReadClientKeyFileRefusesAMissingFile(t *testing.T) {
	_, err := ReadClientKeyFile(filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		t.Fatal("missing file: want an error")
	}
}
