// Command mockryx is the pre-production safety-rehearsal harness for the
// TAIPANBOX agent-governance stack.
//
// Mockryx is a defensive self-test tool, not an attack tool. It exercises
// an operator's own agents inside an isolated pre-production sandbox to
// confirm the operator's own guardrails hold: it replays crafted requests,
// including "hostile" ones such as prompt-injection strings, denied-tool
// requests, and fake secrets, against the operator's own gateway and a
// fake or echo model provider. It never targets, probes, or sends traffic
// to any third-party or external system. It is a fire drill, not a fire.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/TAIPANBOX/mockryx/internal/config"
	"github.com/TAIPANBOX/mockryx/internal/events"
	"github.com/TAIPANBOX/mockryx/internal/report"
	"github.com/TAIPANBOX/mockryx/internal/runner"
	"github.com/TAIPANBOX/mockryx/internal/scenario"
	"github.com/TAIPANBOX/mockryx/internal/watch"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// Process exit codes. They let a CI gate tell a real guardrail gap from a
// misconfigured harness:
//
//	0  every rehearsed guardrail held.
//	1  the run completed and found one or more defensive gaps (a Finding).
//	2  a usage, config, or load error (bad flag, wrong argument count, no
//	   gateway, unreadable scenario) so nothing was actually rehearsed.
const (
	exitOK       = 0
	exitFindings = 1
	exitUsage    = 2
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mockryx:", err)
		os.Exit(exitCode(err))
	}
}

// findingsError reports that a run (or a re-rendered report) completed and
// surfaced defensive gaps. It is the ONLY error that maps to exit code 1;
// every other failure is a usage/config/load error and maps to exit code 2.
type findingsError struct {
	findings  int
	scenarios int
	// skipped counts scenarios whose gap only exists because --fail-on-skip
	// promoted a StatusSkipped result's discarded findings into the total
	// (see runRun). It is 0 in the ordinary case. It only changes Error()'s
	// message, never exitCode's mapping: a strict-mode skip is still
	// squarely in the "gaps" class (exit 1), not usage (exit 2).
	skipped int
}

func (e *findingsError) Error() string {
	if e.skipped > 0 {
		return fmt.Sprintf("%d defensive gap(s) found across %d scenario(s) (including %d skipped-as-not-configured, treated as a gap because --fail-on-skip was set)",
			e.findings, e.scenarios, e.skipped)
	}
	return fmt.Sprintf("%d defensive gap(s) found across %d scenario(s)", e.findings, e.scenarios)
}

// exitCode maps an error returned by run to a process exit code. A
// findingsError (even wrapped) means guardrail gaps were found (1); anything
// else is a misconfigured or misinvoked harness (2).
func exitCode(err error) int {
	if err == nil {
		return exitOK
	}
	var fe *findingsError
	if errors.As(err, &fe) {
		return exitFindings
	}
	return exitUsage
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("no command given")
	}
	switch args[0] {
	case "run":
		return runRun(args[1:])
	case "report":
		return runReport(args[1:])
	case "version":
		fmt.Println("mockryx", version)
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: mockryx <command> [flags]

commands:
  run      run every scenario in a directory against a gateway, print a report
  report   re-render a report previously saved with 'run --save'
  version  print version

Mockryx is a defensive self-test harness: it rehearses an operator's own
guardrails against the operator's own gateway. It never targets an external
or third-party system.`)
}

func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		gatewayFlag    = fs.String("gateway", "", "gateway base URL to rehearse against (default: $MOCKRYX_GATEWAY)")
		apiKeyFlag     = fs.String("api-key", "", "x-api-key sent with every crafted request (default: $MOCKRYX_API_KEY); a value on the command line is on the process list, so prefer --api-key-file")
		apiKeyFileFlag = fs.String("api-key-file", "", "file holding the x-api-key sent with every crafted request (default: $MOCKRYX_API_KEY_FILE); the preferred source, since the key is read from the file and stays off argv")
		keyFileFlag    = fs.String("client-key-file", "", "file holding the gateway client key, sent as x-fuse-key to the gateway (default: $MOCKRYX_CLIENT_KEY_FILE); the key is read from the file, never from a flag value, so it stays off argv")
		agentIDFlag    = fs.String("agent-id", "", "agent id every request presents, replacing each scenario's own (default: $MOCKRYX_AGENT_ID); under a client key it must be inside that key's namespace")
		brokerFlag     = fs.String("mcp-broker", "", "MCP broker base URL that mcp steps are sent to (default: $MOCKRYX_MCP_BROKER); required only if a loaded scenario has an mcp step")
		eventsFlag     = fs.String("events", "", "path to append sim_run/sim_finding/blast_radius_measured events to (default: $MOCKRYX_EVENTS_PATH)")
		watchFlag      watchPathsFlag
		format         = fs.String("format", "human", "report format: human|json")
		savePath       = fs.String("save", "", "also write the JSON report to this path, for 'mockryx report' later")
		failOnSkip     = fs.Bool("fail-on-skip", false, "treat a scenario skipped as not-configured (its guardrail's signal header was never observed) as a defensive gap; off by default, since a genuine skip is not a gap on its own")
	)
	fs.Var(&watchFlag, "watch-events", "a downstream product's own agent-event NDJSON log to poll for expect.event checks (repeatable; default: $MOCKRYX_WATCH_EVENTS, comma-separated)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mockryx run [flags] <scenario-dir>\n\nflags:\n")
		fs.PrintDefaults()
	}
	positionals, err := parseArgsAnyOrder(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		fs.Usage()
		return fmt.Errorf("run requires exactly one scenario directory")
	}
	dir := positionals[0]
	warnAPIKeyFlag(os.Stderr, *apiKeyFlag)

	env := config.FromEnv()
	gateway := firstNonEmpty(*gatewayFlag, env.Gateway)
	if gateway == "" {
		return fmt.Errorf("no gateway configured: set --gateway or $MOCKRYX_GATEWAY")
	}
	apiKey, err := resolveAPIKey(firstNonEmpty(*apiKeyFileFlag, env.APIKeyFile), *apiKeyFlag, env.APIKey)
	if err != nil {
		return err
	}
	clientKey, err := resolveClientKey(firstNonEmpty(*keyFileFlag, env.ClientKeyFile), env.ClientKey)
	if err != nil {
		return err
	}
	identity := runner.Identity{ClientKey: clientKey, AgentID: firstNonEmpty(*agentIDFlag, env.AgentID)}
	mcpBroker := firstNonEmpty(*brokerFlag, env.MCPBroker)
	eventsPath := firstNonEmpty(*eventsFlag, env.EventsPath)
	watchPaths := []string(watchFlag)
	if len(watchPaths) == 0 {
		watchPaths = env.WatchEvents
	}

	scenarios, err := scenario.LoadDir(dir)
	if err != nil {
		return err
	}

	// Fail fast, before sending a single request, if a scenario declares
	// expect.event but no --watch-events/$MOCKRYX_WATCH_EVENTS was given:
	// the alternative is a silent no-op check the operator would only
	// discover by noticing a defensive gap that was never actually looked
	// for, exactly the kind of ambiguity this harness exists to avoid.
	if len(watchPaths) == 0 {
		if s, step, ok := firstEventExpectation(scenarios); ok {
			return fmt.Errorf("scenario %q step %q declares expect.event, but no --watch-events or $MOCKRYX_WATCH_EVENTS was provided", s, step)
		}
	}
	var watcher runner.Watcher
	if len(watchPaths) > 0 {
		watcher = &watch.FileWatcher{Paths: watchPaths}
	}

	// The same fail-fast, for the same reason, for an mcp step: it needs the
	// MCP broker the operator names with --mcp-broker, and a run with the
	// broker missing or not there would otherwise be a run of transport
	// Findings that rehearsed nothing and exited 1, which a CI gate would
	// read as a guardrail gap. It is a usage error, exit 2, before a single
	// request goes to the gateway either. A run with no mcp step never looks
	// at the broker setting at all.
	if s, step, ok := firstMCPStep(scenarios); ok {
		if mcpBroker == "" {
			return fmt.Errorf("scenario %q step %q is an mcp step, but no --mcp-broker or $MOCKRYX_MCP_BROKER was provided", s, step)
		}
		if err := runner.ProbeBroker(mcpBroker); err != nil {
			return fmt.Errorf("the MCP broker %s is unreachable, so scenario %q step %q cannot be rehearsed: %w", mcpBroker, s, step, err)
		}
	}

	emitter, err := events.Open(eventsPath)
	if err != nil {
		return err
	}
	defer emitter.Close()
	if eventsPath != "" {
		if resumed := emitter.ResumedFrom(); resumed != "" {
			fmt.Fprintf(os.Stderr, "mockryx: resumed the event chain in %s from %s\n", eventsPath, events.ChainPreview(resumed))
		} else {
			fmt.Fprintf(os.Stderr, "mockryx: starting a fresh event chain in %s\n", eventsPath)
		}
	}

	runID := fmt.Sprintf("mockryx-%d", time.Now().UTC().UnixNano())
	_ = emitter.SimRun(runID, "start", len(scenarios), 0, gateway)

	rep := report.Report{RunID: runID, Gateway: gateway, Generated: time.Now().UTC()}
	if _, _, ok := firstMCPStep(scenarios); ok {
		rep.MCPBroker = mcpBroker
	}
	for _, s := range scenarios {
		res := runner.RunWithIdentity(s, gateway, mcpBroker, apiKey, identity, watcher)
		rep.Results = append(rep.Results, res)
		for _, f := range res.Findings {
			_ = emitter.SimFinding(events.SimFindingInput{
				RunID:             runID,
				Scenario:          f.Scenario,
				Step:              f.Step,
				Attempt:           f.Attempt,
				ExpectStatus:      f.ExpectStatus,
				ExpectHeader:      f.ExpectHeader,
				GotStatus:         f.GotStatus,
				GotHeaders:        f.GotHeaders,
				Detail:            f.Detail,
				ExpectEventSource: f.ExpectEventSource,
				ExpectEventType:   f.ExpectEventType,
				Kind:              f.Kind,
				Expect:            f.Expect,
				Got:               f.Got,
			})
		}
		_ = emitter.BlastRadiusMeasured(runID, s.Name, res.Metrics.Calls, res.Metrics.BudgetBurnedUSD)
	}

	// --fail-on-skip promotes a StatusSkipped result's discarded
	// SkippedFindings into Findings before anything downstream reads rep: an
	// operator who knows a guardrail must be present cannot tell "absent"
	// from "present but broken enough to let this exact request through"
	// from the wire alone (that ambiguity is exactly why Skipped exists and
	// is not a gap by default), so this is their explicit override for when
	// they know better. Status is left as StatusSkipped, so the report still
	// honestly shows what the runner observed; it is now just paired with a
	// non-zero FINDINGS count, in both the human and JSON report and in
	// TotalFindings, exactly like an ordinary Finding. This does not
	// retroactively emit sim_finding events for the promoted findings: event
	// emission already happened, per scenario, in the loop above.
	skipped := 0
	if *failOnSkip {
		for i := range rep.Results {
			res := &rep.Results[i]
			if res.Status != runner.StatusSkipped {
				continue
			}
			skipped++
			res.Findings = append(res.Findings, res.SkippedFindings...)
			res.SkippedFindings = nil
		}
	}

	total := rep.TotalFindings()
	_ = emitter.SimRun(runID, "end", len(scenarios), total, gateway)

	if err := renderReport(*format, rep); err != nil {
		return err
	}

	if *savePath != "" {
		if err := report.Save(*savePath, rep); err != nil {
			return err
		}
	}

	if total > 0 {
		return &findingsError{findings: total, scenarios: len(scenarios), skipped: skipped}
	}
	return nil
}

func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	format := fs.String("format", "human", "report format: human|json")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mockryx report [flags] <saved-report.json>\n\nflags:\n")
		fs.PrintDefaults()
	}
	positionals, err := parseArgsAnyOrder(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		fs.Usage()
		return fmt.Errorf("report requires exactly one path, from a prior 'run --save'")
	}

	rep, err := report.Load(positionals[0])
	if err != nil {
		return err
	}

	if err := renderReport(*format, rep); err != nil {
		return err
	}

	if total := rep.TotalFindings(); total > 0 {
		return &findingsError{findings: total, scenarios: len(rep.Results)}
	}
	return nil
}

func renderReport(format string, rep report.Report) error {
	switch format {
	case "human":
		report.Human(os.Stdout, rep)
		return nil
	case "json":
		return report.JSON(os.Stdout, rep)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

// parseArgsAnyOrder parses fs while tolerating flags placed either before OR
// after the positional arguments. Go's stdlib flag package stops parsing at the
// first non-flag token, so on its own "run ./scenarios --gateway X" would leave
// --gateway unset and strand ./scenarios: only the flags-first form works. This
// peels off each positional as flag.Parse surfaces it, then re-parses the rest,
// so "run --gateway X ./scenarios" and "run ./scenarios --gateway X" are
// equivalent. It delegates to flag.Parse for every token, so a flag's value
// (e.g. the X in "--gateway X") is never mistaken for a positional. It returns
// the positionals in order; a flag-parsing error (unknown flag, missing value)
// is returned unchanged.
func parseArgsAnyOrder(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positionals, nil
		}
		positionals = append(positionals, rest[0])
		args = rest[1:]
	}
}

// resolveClientKey returns the gateway client key from the one place the
// operator gave it: a key file, or the MOCKRYX_CLIENT_KEY value. Both at once is
// refused, because the two could disagree and the run would not know which
// one it was holding. A key file that cannot be read is refused before any
// request is sent, so a run cannot start unkeyed by accident.
func resolveClientKey(keyFile, envValue string) (string, error) {
	switch {
	case keyFile != "" && envValue != "":
		return "", fmt.Errorf("give the client key once: as a file (--client-key-file or $MOCKRYX_CLIENT_KEY_FILE) or as $MOCKRYX_CLIENT_KEY, not both")
	case keyFile != "":
		return config.ReadClientKeyFile(keyFile)
	}
	return envValue, nil
}

// resolveAPIKey returns the x-api-key from the one place the operator gave it.
// A key file, from the flag or the environment, is the preferred source. It is
// refused alongside a key value (the flag or $MOCKRYX_API_KEY), because the run
// could not tell which one it was meant to hold. With no file, the value is the
// flag's if given and the environment's otherwise, as it always was.
func resolveAPIKey(keyFile, flagValue, envValue string) (string, error) {
	switch {
	case keyFile != "" && (flagValue != "" || envValue != ""):
		return "", fmt.Errorf("give the API key once: as a file (--api-key-file or $MOCKRYX_API_KEY_FILE) or as a value (--api-key or $MOCKRYX_API_KEY), not both")
	case keyFile != "":
		return config.ReadAPIKeyFile(keyFile)
	}
	return firstNonEmpty(flagValue, envValue), nil
}

// warnAPIKeyFlag writes one line when the x-api-key came from the command line,
// where other users can read it in the process list. It never writes the value.
func warnAPIKeyFlag(w io.Writer, flagValue string) {
	if flagValue == "" {
		return
	}
	fmt.Fprintln(w, "mockryx: warning: --api-key puts the x-api-key on the process list, where other users can read it; use --api-key-file or $MOCKRYX_API_KEY_FILE")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// watchPathsFlag implements flag.Value for a repeatable --watch-events
// flag: each occurrence appends one path, matching the --load source:path
// repeatable-flag pattern used elsewhere in the stack (e.g. Idryx/Qryx).
type watchPathsFlag []string

func (f *watchPathsFlag) String() string { return strings.Join(*f, ",") }
func (f *watchPathsFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// firstEventExpectation returns the first scenario/step that declares
// Expect.Event, so runRun's upfront --watch-events validation can name a
// concrete offender in its error rather than just saying "something needs
// it".
func firstEventExpectation(scenarios []scenario.Scenario) (scenarioName, stepName string, ok bool) {
	for _, s := range scenarios {
		for _, step := range s.Steps {
			if step.Expect.Event != nil {
				return s.Name, step.Name, true
			}
		}
	}
	return "", "", false
}

// firstMCPStep returns the first scenario/step that is an mcp step, so
// runRun's upfront --mcp-broker validation can name a concrete offender.
func firstMCPStep(scenarios []scenario.Scenario) (scenarioName, stepName string, ok bool) {
	for _, s := range scenarios {
		for _, step := range s.Steps {
			if step.Kind() == scenario.KindMCP {
				return s.Name, step.Name, true
			}
		}
	}
	return "", "", false
}
