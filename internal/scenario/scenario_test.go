// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

var known = []string{"etcd", "pods", "alerts", "mcp"}

func TestBuiltinsAreValid(t *testing.T) {
	bs := Builtins()
	if len(bs) != 2 || bs[0].Name != "post-upgrade" || bs[1].Name != "pre-upgrade" {
		t.Fatalf("builtins: %+v", bs)
	}
	for _, b := range bs {
		sc, _, src, err := Load(b.Name)
		if err != nil || src != "built-in" {
			t.Fatalf("%s: %v %q", b.Name, err, src)
		}
		if err := sc.Validate(known); err != nil {
			t.Errorf("%s: %v", b.Name, err)
		}
		if b.Description == "" {
			t.Errorf("%s: no description", b.Name)
		}
		// The first step of every built-in scenario is a check: the gate.
		if sc.Steps[0].Check == nil {
			t.Errorf("%s: must start with a check", b.Name)
		}
	}
}

func TestPreUpgradeContent(t *testing.T) {
	sc, _, _, _ := Load("pre-upgrade")
	var kinds []string
	for _, st := range sc.Steps {
		kinds = append(kinds, st.Kind())
	}
	if got := strings.Join(kinds, ","); got != "check,silence,banner,defrag,check" {
		t.Fatalf("pre-upgrade steps: %s", got)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]string{
		"name: x\nsteps: []":                                           "at least one step",
		"steps:\n- check: {}":                                          "name is required",
		"name: x\nsteps:\n- {}":                                        "empty step",
		"name: x\nsteps:\n- check: {}\n  defrag: {}":                   "one operation per step",
		"name: x\nsteps:\n- check: {checks: [nope]}":                   `unknown check "nope"`,
		"name: x\nsteps:\n- banner: {text: ''}":                        "banner text is required",
		"name: x\nsteps:\n- banner: {text: a, location: Middle}":       "banner location",
		"name: x\nsteps:\n- job: {cronJob: backup}":                    "namespace/name",
		"name: x\nsteps:\n- silence: {duration: -1h}":                  "positive",
		"name: x\nsteps:\n- check: {}\n- job: {cronJob: a/b/c}":        "step 2: job cronJob",
		"name: x\nsteps:\n- check: {}\n- removeBanner: {}\n- shell: x": "", // parse error, below
	}
	for doc, want := range cases {
		sc, err := Parse([]byte(doc))
		if want == "" {
			if err == nil {
				t.Errorf("unknown key accepted: %q", doc)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: parse: %v", doc, err)
		}
		if err := sc.Validate(known); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", doc, err, want)
		}
	}
}

func TestLoadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.yaml")
	if err := os.WriteFile(p, []byte("name: mine\nsteps:\n- check: {checks: [etcd]}\n- job: {cronJob: openshift-etcd/etcd-backup, timeout: 30m}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sc, _, src, err := Load(p)
	if err != nil || src != p || sc.Validate(known) != nil {
		t.Fatalf("load: %v %q", err, src)
	}
	if sc.Steps[1].Job.Timeout.Minutes() != 30 || !sc.Steps[1].ChangesCluster() || sc.Steps[0].ChangesCluster() {
		t.Fatalf("job step: %+v", sc.Steps[1].Job)
	}
	if d := sc.Steps[1].Describe(); !strings.Contains(d, "openshift-etcd/etcd-backup") {
		t.Fatal(d)
	}
	for in, want := range map[string]string{"4h": "4h", "90m": "1h30m", "45s": "45s", "10m": "10m"} {
		st := Step{Silence: &SilenceStep{}}
		st.Silence.Duration.Duration, _ = time.ParseDuration(in)
		if d := st.Describe(); !strings.HasSuffix(d, "for "+want) {
			t.Errorf("%s: %q", in, d)
		}
	}
	if _, _, _, err := Load("does-not-exist"); err == nil || !strings.Contains(err.Error(), "pre-upgrade") {
		t.Fatalf("missing scenario: %v", err)
	}
}

// ----------------------------------------------------------------- runner

// fakeRun builds a scenario of n check steps whose results are given by
// levels, and runs it.
func fakeRun(t *testing.T, levels []report.Level, confirm bool, yes bool) (*report.ScenarioRun, []string) {
	t.Helper()
	sc := &Scenario{Name: "t"}
	for i := range levels {
		if i%2 == 0 {
			sc.Steps = append(sc.Steps, Step{Check: &CheckStep{}})
		} else {
			sc.Steps = append(sc.Steps, Step{Defrag: &DefragStep{}})
		}
	}
	var asked []string
	n := 0
	r := &Runner{
		Exec: func(_ context.Context, _ Step) []*report.Section {
			s := report.NewSection("s", "S")
			s.Escalate(levels[n])
			n++
			return []*report.Section{s}
		},
		Confirm: func(q string) bool { asked = append(asked, q); return confirm },
		Yes:     yes,
		Emit:    func(*report.Section) int { return 0 },
	}
	run := NewRun(sc, "test")
	r.Run(context.Background(), sc, run)
	return run, asked
}

func TestRunnerGates(t *testing.T) {
	P, W, C, E := report.Pass, report.Warn, report.Crit, report.Error

	run, asked := fakeRun(t, []report.Level{P, P, P}, false, false)
	if run.Outcome != report.OutcomeCompleted || len(asked) != 0 || !run.Steps[2].Ran {
		t.Fatalf("all green: %+v %v", run, asked)
	}

	run, _ = fakeRun(t, []report.Level{C, P}, false, false)
	if run.Outcome != report.OutcomeStopped || run.Steps[1].Ran || strings.Contains(run.Reason, "still in place") {
		t.Fatalf("critical first check: %+v", run)
	}

	run, _ = fakeRun(t, []report.Level{P, E, P}, false, false)
	if run.Outcome != report.OutcomeStopped || !strings.Contains(run.Reason, "Step 2 failed") ||
		!strings.Contains(run.Reason, "still in place") || run.Steps[2].Ran {
		t.Fatalf("failed action: %+v", run)
	}

	run, asked = fakeRun(t, []report.Level{W, P}, false, false)
	if run.Outcome != report.OutcomeDeclined || len(asked) != 1 || run.Steps[1].Ran {
		t.Fatalf("warning declined: %+v %v", run, asked)
	}

	run, asked = fakeRun(t, []report.Level{W, P}, true, false)
	if run.Outcome != report.OutcomeCompleted || len(asked) != 1 {
		t.Fatalf("warning confirmed: %+v", run)
	}

	run, asked = fakeRun(t, []report.Level{W, W}, false, true)
	if run.Outcome != report.OutcomeCompleted || len(asked) != 0 {
		t.Fatalf("--yes: %+v %v", run, asked)
	}

	// A warning on the last step needs no confirmation.
	run, asked = fakeRun(t, []report.Level{P, W}, false, false)
	if run.Outcome != report.OutcomeCompleted || len(asked) != 0 || run.Steps[1].Status != W {
		t.Fatalf("last step warning: %+v %v", run, asked)
	}

	// --yes never overrides a critical step.
	run, _ = fakeRun(t, []report.Level{C, P}, true, true)
	if run.Outcome != report.OutcomeStopped {
		t.Fatalf("--yes on critical: %+v", run)
	}
}

func TestRunnerCanceled(t *testing.T) {
	sc := &Scenario{Name: "t", Steps: []Step{{Check: &CheckStep{}}, {Defrag: &DefragStep{}}}}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{
		Exec: func(context.Context, Step) []*report.Section {
			cancel()
			return []*report.Section{report.NewSection("s", "S")}
		},
		Emit:    func(*report.Section) int { return 0 },
		Yes:     true,
		Confirm: func(string) bool { return true },
	}
	run := NewRun(sc, "test")
	r.Run(ctx, sc, run)
	if run.Outcome != report.OutcomeCanceled || run.Steps[1].Ran || run.FinishedAt.IsZero() {
		t.Fatalf("canceled: %+v", run)
	}
}
