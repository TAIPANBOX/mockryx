package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const modeTestSecret = "mode-check-secret-value"

// keyFileWithMode writes the one-key file with exactly mode: WriteFile's mode is
// masked by the umask, so Chmod sets it.
func keyFileWithMode(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := writeKeyFile(t, modeTestSecret+"\n")
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertModeRefused checks the refusal has the idryx message shape: it names the
// file and its mode, says who can read it, and never prints the key.
func assertModeRefused(t *testing.T, err error, path string, mode os.FileMode) {
	t.Helper()
	if err == nil {
		t.Errorf("mode %04o: read, want a refusal", mode)
		return
	}
	msg := err.Error()
	for _, want := range []string{path, fmt.Sprintf("%04o", mode), "group or other users can read it", "owner-only"} {
		if !strings.Contains(msg, want) {
			t.Errorf("mode %04o: refusal %q does not contain %q", mode, msg, want)
		}
	}
	if strings.Contains(msg, modeTestSecret) {
		t.Errorf("mode %04o: refusal %q prints the key", mode, msg)
	}
}

func TestReadClientKeyFileRefusesAFileGroupOrOtherCanRead(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		path := keyFileWithMode(t, mode)
		_, err := ReadClientKeyFile(path)
		assertModeRefused(t, err, path, mode)
	}
}

func TestReadAPIKeyFileRefusesAFileGroupOrOtherCanRead(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		path := keyFileWithMode(t, mode)
		_, err := ReadAPIKeyFile(path)
		assertModeRefused(t, err, path, mode)
	}
}

func TestReadKeyFileAcceptsAnOwnerOnlyFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		path := keyFileWithMode(t, mode)
		if got, err := ReadAPIKeyFile(path); err != nil || got != modeTestSecret {
			t.Errorf("mode %04o: got %q, %v; want the key read", mode, got, err)
		}
		if got, err := ReadClientKeyFile(path); err != nil || got != modeTestSecret {
			t.Errorf("mode %04o (client): got %q, %v; want the key read", mode, got, err)
		}
	}
}

// TestReadKeyFileRefusesANonRegularFile: a directory is not a key file, whatever
// its mode, and the refusal says so.
func TestReadKeyFileRefusesANonRegularFile(t *testing.T) {
	dir := t.TempDir()
	for name, read := range map[string]func(string) (string, error){"client": ReadClientKeyFile, "API": ReadAPIKeyFile} {
		_, err := read(dir)
		if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
			t.Errorf("%s key: a directory gave %v, want a refusal that says it is not a regular file", name, err)
		}
	}
}

// TestKeyFileModeCheckIsSkippedOnWindowsByInjection runs on every OS: the Windows
// rule is tested by injecting "windows" as the operating system. On Windows Go
// reports a writable file as 0666 whatever its ACL, so the mode rule cannot be
// applied there; the check is skipped, and that is a named limit, not a pass.
// The non-regular refusal still applies on Windows.
func TestKeyFileModeCheckIsSkippedOnWindowsByInjection(t *testing.T) {
	path := keyFileWithMode(t, 0o644)

	got, err := readKeyFileFor(path, "API key", "windows")
	if err != nil || got != modeTestSecret {
		t.Errorf("windows, mode 0644: got %q, %v; want the key read, the mode is not checked there", got, err)
	}
	if _, err := readKeyFileFor(path, "API key", "linux"); err == nil {
		t.Error("linux, mode 0644: read, want a refusal")
	}
	if _, err := readKeyFileFor(t.TempDir(), "API key", "windows"); err == nil {
		t.Error("windows, a directory: read, want a refusal: the regular-file rule applies on every OS")
	}
}
