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

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mockryx:", err)
		os.Exit(1)
	}
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("run requires exactly one scenario directory")
	}
	dir := fs.Arg(0)

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
		return fmt.Errorf("%d defensive gap(s) found across %d scenario(s)", total, len(scenarios))
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("report requires exactly one path, from a prior 'run --save'")
	}

	rep, err := report.Load(fs.Arg(0))
	if err != nil {
		return err
	}

	if err := renderReport(*format, rep); err != nil {
		return err
	}

	if total := rep.TotalFindings(); total > 0 {
		return fmt.Errorf("%d defensive gap(s) found across %d scenario(s)", total, len(rep.Results))
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
