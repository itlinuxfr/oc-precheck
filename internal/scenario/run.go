// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/itlinuxfr/oc-upkit/internal/checks"
	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// NewRun prepares the record of a scenario run: every step is listed, none
// has run yet.
func NewRun(sc *Scenario, source string) *report.ScenarioRun {
	run := &report.ScenarioRun{Name: sc.Name, Source: source, Outcome: report.OutcomePlanned}
	for i, st := range sc.Steps {
		run.Steps = append(run.Steps, &report.StepRun{
			Index: i + 1, Kind: st.Kind(), Description: st.Describe(), Changes: st.ChangesCluster(),
			Status: report.Skip, // until the step runs
		})
	}
	return run
}

// Runner executes the steps in order and applies the gates:
//   - a step ending in CRIT or ERROR stops the scenario;
//   - a step ending in WARN asks for confirmation (unless Yes is set);
//   - anything else continues.
type Runner struct {
	// Exec performs one step and returns its sections.
	Exec func(ctx context.Context, st Step) []*report.Section
	// Confirm asks the operator a yes/no question.
	Confirm func(question string) bool
	// Yes answers yes to every confirmation.
	Yes bool
	// StepStarted is called before each step (progress display).
	StepStarted func(st *report.StepRun, total int)
	// Emit records a section and returns its index in the report.
	Emit func(s *report.Section) int
	// Now is the clock (tests).
	Now func() time.Time
}

// Run executes the scenario and fills run (from NewRun).
func (r *Runner) Run(ctx context.Context, sc *Scenario, run *report.ScenarioRun) {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	run.StartedAt = now()
	run.Outcome = report.OutcomeCompleted
	defer func() { run.FinishedAt = now() }()

	total := len(sc.Steps)
	for i, step := range sc.Steps {
		sr := run.Steps[i]
		if ctx.Err() != nil {
			run.Outcome, run.Reason = report.OutcomeCanceled, fmt.Sprintf("Interrupted before step %d.", sr.Index)
			return
		}
		if r.StepStarted != nil {
			r.StepStarted(sr, total)
		}
		sr.Ran, sr.Status = true, report.Pass
		for _, s := range r.Exec(ctx, step) {
			sr.Sections = append(sr.Sections, r.Emit(s))
			sr.Status = report.Worse(sr.Status, s.Status)
		}
		if ctx.Err() != nil {
			run.Outcome, run.Reason = report.OutcomeCanceled, fmt.Sprintf("Interrupted during step %d.", sr.Index)
			return
		}
		last := i == total-1
		switch {
		case sr.Status == report.Error:
			run.Outcome, run.Reason = report.OutcomeStopped, fmt.Sprintf("Step %d failed.", sr.Index)
		case sr.Status == report.Crit:
			run.Outcome, run.Reason = report.OutcomeStopped, fmt.Sprintf("Step %d is CRITICAL.", sr.Index)
		case sr.Status == report.Warn && !last && !r.Yes &&
			!r.Confirm(fmt.Sprintf("Step %d ended with warnings. Continue with step %d (%s)?", sr.Index, sr.Index+1, run.Steps[i+1].Description)):
			run.Outcome, run.Reason = report.OutcomeDeclined, fmt.Sprintf("Stopped after step %d (warnings).", sr.Index)
		default:
			continue
		}
		if hasChanges(run.Steps[:i+1]) {
			run.Reason += " Changes already made are still in place."
		}
		return
	}
}

func hasChanges(steps []*report.StepRun) bool {
	for _, s := range steps {
		if s.Ran && s.Changes {
			return true
		}
	}
	return false
}

// Executor performs the steps against a cluster with the actions of the
// checks package.
type Executor struct {
	Env    *checks.Env
	Opts   *checks.Options
	Author string    // silence author
	Log    io.Writer // live progress of long actions
	// RunChecks runs the read-only checks selected by a check step.
	RunChecks func(ctx context.Context, step CheckStep) ([]*report.Section, error)
}

// Exec performs one step.
func (e *Executor) Exec(ctx context.Context, st Step) []*report.Section {
	one := func(s *report.Section) []*report.Section { return []*report.Section{s} }
	switch {
	case st.Check != nil:
		secs, err := e.RunChecks(ctx, *st.Check)
		if err != nil {
			return one(report.NewSection("checks", "Checks").Fail(err, ""))
		}
		return secs

	case st.Silence != nil:
		if len(st.Silence.Matchers) == 0 {
			if sil, err := checks.ToolSilences(ctx, e.Env, e.Opts); err == nil {
				for _, x := range sil {
					if x.MatchesAll() {
						s := report.NewSection("silence", "Action: Alertmanager silence")
						s.Summary = fmt.Sprintf("A silence created by oc-upkit is already active until %s", x.EndsAt.Local().Format("2006-01-02 15:04"))
						s.Add(report.Info, "Kept silence %s, nothing created", x.ID)
						return one(s)
					}
				}
			}
		}
		so := checks.SilenceOptions{
			Duration: or(st.Silence.Duration.Duration, DefaultSilenceDuration),
			Author:   firstOf(st.Silence.Author, e.Author),
			Comment:  firstOf(st.Silence.Comment, DefaultSilenceComment),
			Matchers: st.Silence.Matchers,
		}
		return one(checks.ActionSilence(ctx, e.Env, e.Opts, so))

	case st.Banner != nil:
		b := st.Banner
		return one(checks.ActionBanner(ctx, e.Env, checks.BannerOptions{
			Name: b.Name, Text: b.Text, Location: b.Location,
			Color: firstOf(b.Color, "#fff"), Background: firstOf(b.Background, "#c9190b"),
			LinkURL: b.Link, LinkText: b.LinkText,
		}))

	case st.Defrag != nil:
		d := st.Defrag
		return one(checks.ActionDefrag(ctx, e.Env, e.Opts, checks.DefragOptions{
			MinFragmentation: d.MinFragmentation,
			Pause:            or(d.Pause.Duration, DefaultDefragPause),
			CommandTimeout:   or(d.CommandTimeout.Duration, DefaultDefragTimeout),
			DisarmAlarms:     d.DisarmAlarms,
			Log:              e.Log,
		}))

	case st.Job != nil:
		return one(checks.ActionJobFromCronJob(ctx, e.Env, checks.JobOptions{
			CronJob: st.Job.CronJob, Timeout: or(st.Job.Timeout.Duration, DefaultJobTimeout), Log: e.Log,
		}))

	case st.RemoveBanner != nil:
		names := st.RemoveBanner.Names
		return one(checks.ActionRemoveBanner(ctx, e.Env, names, len(names) == 0, false))

	case st.ExpireSilences != nil:
		return one(checks.ActionExpireToolSilences(ctx, e.Env, e.Opts, false))
	}
	return one(report.NewSection("step", "Step").Fail(fmt.Errorf("empty step"), ""))
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
