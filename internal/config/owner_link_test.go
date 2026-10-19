package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadKeyFileJudgesTheTargetOfASymlink: a Kubernetes Secret volume presents
// each key as a symlink to a file under ..data, so the rule must judge the file the
// link leads to, after following it. Real files, owned by this user: a link to a
// 0600 file is read; a link to a 0640 file is refused.
func TestReadKeyFileJudgesTheTargetOfASymlink(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	bad := filepath.Join(dir, "bad")
	for path, mode := range map[string]os.FileMode{good: 0o600, bad: 0o640} {
		if err := os.WriteFile(path, []byte(modeTestSecret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	linkGood := filepath.Join(dir, "link-good")
	linkBad := filepath.Join(dir, "link-bad")
	if err := os.Symlink(good, linkGood); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bad, linkBad); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadAPIKeyFile(linkGood); err != nil || got != modeTestSecret {
		t.Errorf("link to a 0600 file: got %q, %v; want the key read", got, err)
	}
	if _, err := ReadAPIKeyFile(linkBad); err == nil {
		t.Error("link to a 0640 file owned by this user: read, want a refusal of the target's mode")
	}
}
