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
	"os"
	"time"

	"github.com/TAIPANBOX/mockryx/internal/config"
	"github.com/TAIPANBOX/mockryx/internal/events"
	"github.com/TAIPANBOX/mockryx/internal/report"
	"github.com/TAIPANBOX/mockryx/internal/runner"
	"github.com/TAIPANBOX/mockryx/internal/scenario"
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
}

func (e *findingsError) Error() string {
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
		gatewayFlag = fs.String("gateway", "", "gateway base URL to rehearse against (default: $MOCKRYX_GATEWAY)")
		apiKeyFlag  = fs.String("api-key", "", "x-api-key sent with every crafted request (default: $MOCKRYX_API_KEY)")
		eventsFlag  = fs.String("events", "", "path to append sim_run/sim_finding/blast_radius_measured events to (default: $MOCKRYX_EVENTS_PATH)")
		format      = fs.String("format", "human", "report format: human|json")
		savePath    = fs.String("save", "", "also write the JSON report to this path, for 'mockryx report' later")
	)
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

	env := config.FromEnv()
	gateway := firstNonEmpty(*gatewayFlag, env.Gateway)
	if gateway == "" {
		return fmt.Errorf("no gateway configured: set --gateway or $MOCKRYX_GATEWAY")
	}
	apiKey := firstNonEmpty(*apiKeyFlag, env.APIKey)
	eventsPath := firstNonEmpty(*eventsFlag, env.EventsPath)

	scenarios, err := scenario.LoadDir(dir)
	if err != nil {
		return err
	}

	emitter, err := events.Open(eventsPath)
	if err != nil {
		return err
	}
	defer emitter.Close()

	runID := fmt.Sprintf("mockryx-%d", time.Now().UTC().UnixNano())
	_ = emitter.SimRun(runID, "start", len(scenarios), 0, gateway)

	rep := report.Report{RunID: runID, Gateway: gateway, Generated: time.Now().UTC()}
	for _, s := range scenarios {
		res := runner.Run(s, gateway, apiKey)
		rep.Results = append(rep.Results, res)
		for _, f := range res.Findings {
			_ = emitter.SimFinding(events.SimFindingInput{
				RunID:        runID,
				Scenario:     f.Scenario,
				Step:         f.Step,
				Attempt:      f.Attempt,
				ExpectStatus: f.ExpectStatus,
				ExpectHeader: f.ExpectHeader,
				GotStatus:    f.GotStatus,
				GotHeaders:   f.GotHeaders,
				Detail:       f.Detail,
			})
		}
		_ = emitter.BlastRadiusMeasured(runID, s.Name, res.Metrics.Calls, res.Metrics.BudgetBurnedUSD)
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
		return &findingsError{findings: total, scenarios: len(scenarios)}
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
