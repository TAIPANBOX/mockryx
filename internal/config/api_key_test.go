package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFromEnvReadsTheAPIKeyFileVariable(t *testing.T) {
	t.Setenv("MOCKRYX_API_KEY_FILE", "/run/secrets/mockryx-api-key")

	if c := FromEnv(); c.APIKeyFile != "/run/secrets/mockryx-api-key" {
		t.Fatalf("APIKeyFile = %q, want the path from MOCKRYX_API_KEY_FILE", c.APIKeyFile)
	}
}

func TestReadAPIKeyFileReturnsTheKeyWithoutTheTrailingNewline(t *testing.T) {
	got, err := ReadAPIKeyFile(writeKeyFile(t, "api-key-value-0001\n"))
	if err != nil {
		t.Fatalf("ReadAPIKeyFile: %v", err)
	}
	if got != "api-key-value-0001" {
		t.Errorf("key = %q, want the key without its newline", got)
	}
}

// The same file rules as the client key: one key on one line, and an error
// that names the path and never the contents.
func TestReadAPIKeyFileRefusesAFileThatIsNotOneKey(t *testing.T) {
	const secret = "apiPartOneXYZ apiPartTwoXYZ"
	for _, content := range []string{"", "\n", "  \r\n", secret + "\n"} {
		_, err := ReadAPIKeyFile(writeKeyFile(t, content))
		if err == nil {
			t.Errorf("content %q: want a refusal, got none", content)
			continue
		}
		if strings.Contains(err.Error(), "apiPartOneXYZ") {
			t.Errorf("content %q: error %q repeats the file's contents", content, err)
		}
	}
}

func TestReadAPIKeyFileRefusesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	_, err := ReadAPIKeyFile(path)
	if err == nil {
		t.Fatal("missing file: want an error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file", err)
	}
}
