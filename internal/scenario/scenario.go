// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

// Package scenario describes and runs scenarios: ordered lists of checks and
// maintenance actions, with gates between the steps.
//
// A scenario only chains the operations of the tool itself; it never runs
// arbitrary commands, and it never starts the upgrade.
package scenario

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/itlinuxfr/oc-upkit/internal/checks"
)

// Scenario is an ordered list of steps.
type Scenario struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Steps       []Step `json:"steps"`
}

// Step holds exactly one operation.
type Step struct {
	Check          *CheckStep          `json:"check,omitempty"`
	Silence        *SilenceStep        `json:"silence,omitempty"`
	Banner         *BannerStep         `json:"banner,omitempty"`
	Defrag         *DefragStep         `json:"defrag,omitempty"`
	Job            *JobStep            `json:"job,omitempty"`
	RemoveBanner   *RemoveBannerStep   `json:"removeBanner,omitempty"`
	ExpireSilences *ExpireSilencesStep `json:"expireSilences,omitempty"`
}

// CheckStep runs read-only checks. Empty lists mean every check.
type CheckStep struct {
	Checks []string `json:"checks,omitempty"`
	Skip   []string `json:"skip,omitempty"`
}

// SilenceStep creates an Alertmanager silence. It is skipped when a silence
// created by the tool is already active.
type SilenceStep struct {
	Duration checks.Duration `json:"duration,omitempty"`
	Comment  string          `json:"comment,omitempty"`
	Author   string          `json:"author,omitempty"`
	Matchers []string        `json:"matchers,omitempty"`
}

// BannerStep creates or updates the web console banner.
type BannerStep struct {
	Text       string `json:"text"`
	Name       string `json:"name,omitempty"`
	Location   string `json:"location,omitempty"`
	Color      string `json:"color,omitempty"`
	Background string `json:"background,omitempty"`
	Link       string `json:"link,omitempty"`
	LinkText   string `json:"linkText,omitempty"`
}

// DefragStep defragments etcd member by member, leader last.
type DefragStep struct {
	MinFragmentation float64         `json:"minFragmentation,omitempty"`
	Pause            checks.Duration `json:"pause,omitempty"`
	CommandTimeout   checks.Duration `json:"commandTimeout,omitempty"`
	DisarmAlarms     bool            `json:"disarmAlarms,omitempty"`
}

// JobStep runs a Job from a CronJob and waits for it to succeed.
type JobStep struct {
	CronJob string          `json:"cronJob"`
	Timeout checks.Duration `json:"timeout,omitempty"`
}

// RemoveBannerStep removes console banners: the named ones, or every banner
// created by the tool.
type RemoveBannerStep struct {
	Names []string `json:"names,omitempty"`
}

// ExpireSilencesStep expires every active silence created by the tool.
type ExpireSilencesStep struct{}

// Defaults applied to the steps that leave a field empty.
const (
	DefaultSilenceDuration = 4 * time.Hour
	DefaultSilenceComment  = "OpenShift upgrade in progress (oc-upkit)"
	DefaultDefragPause     = 15 * time.Second
	DefaultDefragTimeout   = 60 * time.Second
	DefaultJobTimeout      = time.Hour
)

// Kind returns the name of the operation held by the step.
func (s Step) Kind() string {
	k, _ := s.kinds()
	return k
}

func (s Step) kinds() (string, int) {
	var names []string
	add := func(set bool, n string) {
		if set {
			names = append(names, n)
		}
	}
	add(s.Check != nil, "check")
	add(s.Silence != nil, "silence")
	add(s.Banner != nil, "banner")
	add(s.Defrag != nil, "defrag")
	add(s.Job != nil, "job")
	add(s.RemoveBanner != nil, "removeBanner")
	add(s.ExpireSilences != nil, "expireSilences")
	return strings.Join(names, "+"), len(names)
}

// ChangesCluster reports whether the step modifies the cluster.
func (s Step) ChangesCluster() bool { return s.Check == nil }

// Describe returns a one-line, human-readable description of the step.
func (s Step) Describe() string {
	switch {
	case s.Check != nil:
		d := "Run all checks"
		if len(s.Check.Checks) > 0 {
			d = "Run checks: " + strings.Join(s.Check.Checks, ", ")
		}
		if len(s.Check.Skip) > 0 {
			d += " (skip " + strings.Join(s.Check.Skip, ", ") + ")"
		}
		return d
	case s.Silence != nil:
		d := s.Silence.Duration.Duration
		if d == 0 {
			d = DefaultSilenceDuration
		}
		what := "every alert"
		if len(s.Silence.Matchers) > 0 {
			what = strings.Join(s.Silence.Matchers, " ")
		}
		return fmt.Sprintf("Silence %s for %s", what, shortDuration(d))
	case s.Banner != nil:
		return fmt.Sprintf("Show the console banner %q", s.Banner.Text)
	case s.Defrag != nil:
		return "Defragment etcd, member by member, leader last"
	case s.Job != nil:
		return "Run a Job from CronJob " + s.Job.CronJob + " and wait for it"
	case s.RemoveBanner != nil:
		if len(s.RemoveBanner.Names) > 0 {
			return "Remove the console banner(s) " + strings.Join(s.RemoveBanner.Names, ", ")
		}
		return "Remove the console banners created by oc-upkit"
	case s.ExpireSilences != nil:
		return "Expire the silences created by oc-upkit"
	}
	return "(empty step)"
}

// Validate checks the structure of a scenario. knownChecks lists the valid
// check names.
func (sc *Scenario) Validate(knownChecks []string) error {
	var errs []error
	if strings.TrimSpace(sc.Name) == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if len(sc.Steps) == 0 {
		errs = append(errs, errors.New("at least one step is required"))
	}
	known := map[string]bool{}
	for _, c := range knownChecks {
		known[c] = true
	}
	for i, st := range sc.Steps {
		at := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("step %d: "+format, append([]any{i + 1}, a...)...))
		}
		switch k, n := st.kinds(); {
		case n == 0:
			at("empty step (write e.g. `- check: {}`)")
			continue
		case n > 1:
			at("one operation per step, got %s", k)
			continue
		}
		switch {
		case st.Check != nil:
			for _, c := range append(append([]string{}, st.Check.Checks...), st.Check.Skip...) {
				if !known[c] {
					at("unknown check %q", c)
				}
			}
		case st.Silence != nil:
			if st.Silence.Duration.Duration < 0 {
				at("silence duration must be positive")
			}
		case st.Banner != nil:
			if strings.TrimSpace(st.Banner.Text) == "" {
				at("banner text is required")
			}
			switch st.Banner.Location {
			case "", "BannerTop", "BannerBottom", "BannerTopBottom":
			default:
				at("banner location must be BannerTop, BannerBottom or BannerTopBottom")
			}
		case st.Job != nil:
			ns, name, ok := strings.Cut(st.Job.CronJob, "/")
			if !ok || ns == "" || name == "" || strings.Contains(name, "/") {
				at("job cronJob must be namespace/name, got %q", st.Job.CronJob)
			}
			if st.Job.Timeout.Duration < 0 {
				at("job timeout must be positive")
			}
		}
	}
	return errors.Join(errs...)
}

// Parse decodes a scenario document. Unknown keys are rejected to catch typos.
func Parse(raw []byte) (*Scenario, error) {
	var sc Scenario
	if err := yaml.UnmarshalStrict(raw, &sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

//go:embed builtin/*.yaml
var builtinFS embed.FS

// Builtin is a scenario shipped with the tool.
type Builtin struct {
	Name        string
	Description string
	Source      []byte // YAML, comments included
}

// Builtins returns the scenarios shipped with the tool, sorted by name.
func Builtins() []Builtin {
	entries, _ := builtinFS.ReadDir("builtin")
	var out []Builtin
	for _, e := range entries {
		raw, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			continue
		}
		sc, err := Parse(raw)
		if err != nil {
			panic(fmt.Sprintf("builtin scenario %s: %v", e.Name(), err)) // caught by the tests
		}
		out = append(out, Builtin{Name: sc.Name, Description: sc.Description, Source: raw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Load returns a built-in scenario by name, or reads a scenario file. The
// returned source is the raw YAML and where it came from.
func Load(ref string) (sc *Scenario, raw []byte, source string, err error) {
	for _, b := range Builtins() {
		if b.Name == ref {
			sc, err = Parse(b.Source)
			return sc, b.Source, "built-in", err
		}
	}
	raw, err = os.ReadFile(ref)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			var names []string
			for _, b := range Builtins() {
				names = append(names, b.Name)
			}
			return nil, nil, "", fmt.Errorf("%q is neither a built-in scenario (%s) nor a file", ref, strings.Join(names, ", "))
		}
		return nil, nil, "", err
	}
	sc, err = Parse(raw)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", ref, err)
	}
	return sc, raw, ref, nil
}

// shortDuration renders 4h0m0s as 4h and 1h30m0s as 1h30m.
func shortDuration(d time.Duration) string {
	out := d.String()
	if strings.HasSuffix(out, "m0s") {
		out = strings.TrimSuffix(out, "0s")
	}
	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}
	return out
}
