// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"os/user"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/itlinuxfr/oc-upkit/internal/checks"
	"github.com/itlinuxfr/oc-upkit/internal/report"
	"github.com/itlinuxfr/oc-upkit/internal/scenario"
	"github.com/itlinuxfr/oc-upkit/internal/version"
)

func newRunCommand(g *globals) *cobra.Command {
	var printYAML bool
	var act actionFlags
	cmd := &cobra.Command{
		Use:   "run [SCENARIO | FILE]",
		Short: "Run a scenario: checks and maintenance actions, step by step",
		Long: `Run a scenario: an ordered list of checks and maintenance actions with a
gate after each step.

  - a step ending CRITICAL (or failing) stops the scenario;
  - a step ending with warnings asks for confirmation (--yes to continue);
  - the plan is shown first, and nothing runs before you confirm it.

Built-in scenarios: pre-upgrade and post-upgrade. To adapt one, export it
with --print, edit the file, and run the file. Without argument, the built-in
scenarios are listed.

oc-upkit never starts the upgrade itself: run "oc adm upgrade" once the
pre-upgrade scenario is completed.

Exit codes: 0 completed, 1 stopped at your request, 2 stopped on a critical
step, 3 usage or scenario error, 4 a step failed or the run was interrupted.`,
		Example: g.example(
			"$CMD run",
			"$CMD run pre-upgrade --dry-run",
			"$CMD run pre-upgrade",
			"$CMD run pre-upgrade --print > my-pre-upgrade.yaml",
			"$CMD run my-pre-upgrade.yaml -o json > pre-upgrade-report.json",
			"$CMD run post-upgrade --yes",
		),
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return listScenarios(g)
			}
			return runScenario(g, args[0], printYAML, act)
		},
	}
	cmd.Flags().BoolVar(&printYAML, "print", false, "Print the scenario as YAML and exit (to customize a built-in one)")
	cmd.Flags().BoolVar(&act.dryRun, "dry-run", false, "Show the plan without running anything")
	cmd.Flags().BoolVarP(&act.yes, "yes", "y", false, "Do not ask for confirmation (critical steps still stop the scenario)")
	return cmd
}

func listScenarios(g *globals) error {
	w := tabwriter.NewWriter(g.streams.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Built-in scenarios:")
	for _, b := range scenario.Builtins() {
		fmt.Fprintf(w, "  %s\t%s\n", b.Name, b.Description)
	}
	fmt.Fprintf(w, "\nRun one with: %s run NAME (--dry-run to see the plan)\n", g.name)
	return w.Flush()
}

func checkNames() []string {
	var names []string
	for _, c := range checks.All() {
		names = append(names, c.Name)
	}
	return names
}

func runScenario(g *globals, ref string, printYAML bool, act actionFlags) error {
	sc, raw, source, err := scenario.Load(ref)
	if err != nil {
		return usageError("%w", err)
	}
	if err := sc.Validate(checkNames()); err != nil {
		return usageError("invalid scenario %s:\n%w", ref, err)
	}
	if printYAML {
		_, err := g.streams.Out.Write(raw)
		return err
	}

	ss, err := g.connect()
	if err != nil {
		return err
	}
	defer ss.cancel()

	run := scenario.NewRun(sc, source)
	run.Tool = "oc-upkit " + version.Get().Version
	if u, err := user.Current(); err == nil {
		run.User = u.Username
	}
	ss.rep.Scenario = run
	ss.header("oc-upkit — scenario " + sc.Name)
	if g.text() {
		ss.pr.ScenarioPlan(run)
	}

	if act.dryRun {
		return ss.finishScenario()
	}
	changes := 0
	for _, st := range sc.Steps {
		if st.ChangesCluster() {
			changes++
		}
	}
	if changes > 0 && !act.yes && !g.confirm(fmt.Sprintf("Cluster %s (%s): run scenario %s — %d step(s), %d of them change the cluster?",
		ss.kc.Context, ss.kc.Server, sc.Name, len(sc.Steps), changes)) {
		run.Outcome, run.Reason = report.OutcomeDeclined, "Not started."
		return ss.finishScenario()
	}

	o := g.o
	exec := &scenario.Executor{
		Env: ss.env, Opts: o, Author: authorOrDefault(""), Log: g.streams.ErrOut,
		RunChecks: func(ctx context.Context, only, skip []string) ([]*report.Section, error) {
			if len(only) == 0 && len(skip) == 0 && g.cfg != nil {
				only, skip = g.cfg.Settings.Checks, g.cfg.Settings.Skip
			}
			selected, err := selectChecks(checks.All(), only, skip, len(o.CustomRules) > 0)
			if err != nil {
				return nil, err
			}
			return runChecks(ctx, ss.env, o, selected), nil
		},
	}
	r := &scenario.Runner{
		Exec:    exec.Exec,
		Confirm: g.confirm,
		Yes:     act.yes,
		Emit: func(s *report.Section) int {
			ss.add(s)
			return len(ss.rep.Sections) - 1
		},
		StepStarted: func(st *report.StepRun, total int) {
			if g.text() {
				ss.pr.StepHeader(st, total)
			} else {
				fmt.Fprintf(g.streams.ErrOut, "Step %d/%d: %s\n", st.Index, total, st.Description)
			}
		},
	}
	r.Run(ss.ctx, sc, run)
	return ss.finishScenario()
}

// finishScenario renders the report of a scenario and converts its outcome
// into an exit code.
func (s *session) finishScenario() error {
	s.rep.Compute()
	run := s.rep.Scenario
	var err error
	switch s.g.output {
	case "json":
		err = report.JSON(s.g.streams.Out, s.rep)
	case "yaml":
		err = report.YAML(s.g.streams.Out, s.rep)
	default:
		s.pr.ScenarioSummary(run)
	}
	if err != nil {
		return &ExitError{Code: CodeError, Err: err}
	}
	switch run.Outcome {
	case report.OutcomeDeclined:
		return &ExitError{Code: CodeWarn}
	case report.OutcomeCanceled:
		return &ExitError{Code: CodeError}
	case report.OutcomeStopped:
		if s.rep.Status == report.Error {
			return &ExitError{Code: CodeError}
		}
		return &ExitError{Code: CodeCrit}
	}
	return nil
}
