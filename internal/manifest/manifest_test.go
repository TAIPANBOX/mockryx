// The declaration in components.json is only worth reading if this repository
// proves it, and proves it by RUNNING rather than by describing.
//
// estate-gates cannot do this. It has no Go toolchain, and building twenty-two
// repositories in its CI is a matrix it does not have. This repository already
// runs its suite on every push, so the marginal cost of a few process starts is
// seconds.
//
// What is proved here is exactly the `checked` bucket and nothing else. The
// `declared` bucket is not asserted against anything, on purpose: a test that
// pretended to verify a sentence about purpose would be the failure this whole
// design exists to avoid.
package manifest

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type envVar struct {
	Required bool `json:"required"`
}

type component struct {
	Name    string `json:"name"`
	Class   string `json:"class"`
	Checked struct {
		Package                           string            `json:"package"`
		Subcommand                        string            `json:"subcommand"`
		Env                               map[string]envVar `json:"env"`
		RefusesForConfigurationExitCode   int               `json:"refuses_for_configuration_exit_code"`
		FindingsExitCode                  int               `json:"findings_exit_code"`
		RefusesBeforeContactingTheGateway bool              `json:"refuses_before_contacting_the_gateway"`
	} `json:"checked"`
}

type manifest struct {
	Schema     string      `json:"schema"`
	Repo       string      `json:"repo"`
	Components []component `json:"components"`
}

func root(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func load(t *testing.T) (manifest, string) {
	t.Helper()
	r := root(t)
	b, err := os.ReadFile(filepath.Join(r, "components.json"))
	if err != nil {
		t.Fatalf("reading components.json: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parsing components.json: %v", err)
	}
	if len(m.Components) == 0 {
		t.Fatal("components.json declares no component, so every test here measured nothing")
	}
	return m, r
}

func tool(t *testing.T, m manifest) component {
	t.Helper()
	for _, c := range m.Components {
		if c.Class == "tool" {
			return c
		}
	}
	t.Fatal("components.json declares no tool, so the running half measured nothing")
	return component{}
}

func build(t *testing.T, r, pkg string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mockryx")
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Dir = r
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", pkg, err, out)
	}
	return bin
}

// Runs it and returns the exit code, distinguishing "did not start" from "exited
// with a status", because only the second is a contract.
func status(t *testing.T, bin string, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), string(out)
	}
	t.Fatalf("running %v: %v", args, err)
	return -1, ""
}

// THE ONE THAT CLOSES THE HOLE. A binary this repository builds and does not
// declare is invisible from outside by construction, which is what estate-gates
// invariant 18 says about its own `runs` field.
func TestEveryBinaryThisRepositoryBuildsIsDeclaredAndTheReverse(t *testing.T) {
	m, r := load(t)

	list := exec.Command("go", "list", "-f", "{{if eq .Name \"main\"}}{{.ImportPath}}{{end}}", "./...")
	// Without this the command runs in THIS package's directory and `./...`
	// means this package alone. It then finds no main package, and the test
	// passes while measuring nothing.
	list.Dir = r
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	built := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			built[line] = true
		}
	}
	if len(built) == 0 {
		t.Fatal("go list found no main package in this repository, so this measured nothing")
	}

	declared := map[string]bool{}
	for _, c := range m.Components {
		if c.Checked.Package == "" {
			t.Errorf("component %q declares no package", c.Name)
			continue
		}
		declared[c.Checked.Package] = true
	}
	for p := range built {
		if !declared[p] {
			t.Errorf("this repository builds %s and components.json does not declare it.\n"+
				"A component nobody declares is one no deployment can be asked to install.", p)
		}
	}
	for p := range declared {
		if !built[p] {
			t.Errorf("components.json declares %s and this repository does not build it", p)
		}
	}
}

// A declared subcommand is one the binary actually dispatches on.
func TestEveryDeclaredSubcommandIsOneTheBinaryDispatchesOn(t *testing.T) {
	m, r := load(t)

	b, err := os.ReadFile(filepath.Join(r, "cmd", "mockryx", "main.go"))
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	known := map[string]bool{}
	for _, hit := range regexp.MustCompile(`(?m)^\tcase "([a-z-]+)":`).FindAllStringSubmatch(string(b), -1) {
		known[hit[1]] = true
	}
	if len(known) == 0 {
		t.Fatal("main.go no longer dispatches with a top-level `case \"...\":`, so this measured nothing")
	}
	checked := 0
	for _, c := range m.Components {
		if c.Checked.Subcommand == "" {
			continue
		}
		checked++
		if !known[c.Checked.Subcommand] {
			t.Errorf("components.json says %s runs `mockryx %s` and main.go dispatches no such subcommand",
				c.Name, c.Checked.Subcommand)
		}
	}
	if checked == 0 {
		t.Fatal("no component declares a subcommand, so this measured nothing")
	}
}

// Every MOCKRYX_ name in non-test source, against every one declared.
//
// It reads STRING LITERALS rather than walking calls to os.Getenv: these are
// documented as flag fallbacks in the flag definitions themselves, so a reader
// that followed call sites would report a set that is quietly short. A name
// ending in `_` is a prefix fragment from a doc comment, not a variable.
func TestEveryEnvironmentVariableThisRepositoryReadsIsDeclaredAndTheReverse(t *testing.T) {
	m, r := load(t)

	name := regexp.MustCompile(`MOCKRYX_[A-Z0-9_]+`)
	inSource := map[string]bool{}
	err := filepath.Walk(r, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, n := range name.FindAllString(string(b), -1) {
			if !strings.HasSuffix(n, "_") {
				inSource[n] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if len(inSource) == 0 {
		t.Fatal("no MOCKRYX_ name found in any non-test .go file, so this measured nothing")
	}

	declared := map[string]bool{}
	for _, c := range m.Components {
		for k := range c.Checked.Env {
			declared[k] = true
		}
	}
	var missing, extra []string
	for n := range inSource {
		if !declared[n] {
			missing = append(missing, n)
		}
	}
	for n := range declared {
		if !inSource[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, n := range missing {
		t.Errorf("the code reads %s and components.json does not declare it", n)
	}
	for _, n := range extra {
		t.Errorf("components.json declares %s and no non-test source reads it", n)
	}
}

// AND THE HALF NO CENTRAL FILE COULD EVER DO: the exit-code contract, which
// another repository reads.
//
// stack-up's routine dispatches on it: 0 is ok, 1 is findings, and anything else
// is an error shown to the operator. So the difference between "I would not
// start" and "I ran and it went badly" is a cross-repository interface.
//
// Three refusals, all the same code, and the third is the one that matters: it
// still refuses when a gateway IS configured, which is what makes this
// fail-before-half-the-job rather than merely fail-fast.
func TestTheExitCodeContractOtherRepositoriesReadIsTheDeclaredOne(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	c := tool(t, m)
	want := c.Checked.RefusesForConfigurationExitCode
	if want == 0 {
		t.Fatal("components.json declares no configuration-refusal exit code, so this measured nothing")
	}
	bin := build(t, r, c.Checked.Package)
	sub := c.Checked.Subcommand

	// 1. No scenario directory at all.
	if got, out := status(t, bin, []string{}, sub); got != want {
		t.Errorf("`mockryx %s` with no scenario directory exited %d; components.json says %d\n%s",
			sub, got, want, out)
	}

	// 2. Scenarios, no gateway.
	if got, out := status(t, bin, []string{}, sub, filepath.Join(r, "scenarios")); got != want {
		t.Errorf("`mockryx %s <dir>` with no gateway exited %d; components.json says %d\n%s",
			sub, got, want, out)
	}

	// 3. Scenarios AND a gateway, but a scenario needs an event log and none was
	// given. The gateway points at a port nothing listens on, so a refusal here
	// cannot have come from talking to it.
	if c.Checked.RefusesBeforeContactingTheGateway {
		env := []string{"MOCKRYX_GATEWAY=http://127.0.0.1:1/unreachable"}
		got, out := status(t, bin, env, sub, filepath.Join(r, "scenarios"))
		if got != want {
			t.Errorf("with a gateway configured and no event log it exited %d; components.json says %d.\n"+
				"That is the claim that this refuses on its whole configuration before it "+
				"contacts anything.\n%s", got, want, out)
		}
		if strings.Contains(out, "scenario(s) against") {
			t.Errorf("it began the drill before refusing:\n%s", out)
		}
	}

	// And the other half of the contract: fully configured, unreachable gateway,
	// which is a drill that RAN and found something rather than one that refused.
	if want := c.Checked.FindingsExitCode; want != 0 {
		log := filepath.Join(t.TempDir(), "events.ndjson")
		if err := os.WriteFile(log, nil, 0o600); err != nil {
			t.Fatalf("writing the event log: %v", err)
		}
		env := []string{
			"MOCKRYX_GATEWAY=http://127.0.0.1:1/unreachable",
			"MOCKRYX_WATCH_EVENTS=" + log,
		}
		got, out := status(t, bin, env, sub, filepath.Join(r, "scenarios"))
		if got != want {
			t.Errorf("fully configured against an unreachable gateway it exited %d; "+
				"components.json says findings are %d\n%s", got, want, out)
		}
		if !strings.Contains(out, "scenario(s) against") {
			t.Errorf("it never began the drill, so this proved a refusal rather than findings:\n%s", out)
		}
	}
}
