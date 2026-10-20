// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

// Package report holds the result model shared by every check and the
// text / JSON / YAML renderers.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"sigs.k8s.io/yaml"
)

// Level is the severity of a finding, a table row or a whole section.
type Level int

// Levels, ordered from the least to the most severe.
const (
	Pass Level = iota
	Info
	Skip
	Warn
	Crit
	Error
)

var levelNames = map[Level]string{
	Pass: "PASS", Info: "INFO", Skip: "SKIP", Warn: "WARN", Crit: "CRIT", Error: "ERROR",
}

func (l Level) String() string { return levelNames[l] }

// MarshalJSON renders the level as its name.
func (l Level) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }

// UnmarshalJSON parses a level name.
func (l *Level) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	for k, v := range levelNames {
		if v == s {
			*l = k
			return nil
		}
	}
	return fmt.Errorf("unknown level %q", s)
}

// Worse returns the most severe of two levels.
func Worse(a, b Level) Level {
	if b > a {
		return b
	}
	return a
}

// Finding is one notable fact produced by a check.
type Finding struct {
	Level   Level  `json:"level"`
	Rule    string `json:"rule,omitempty"`
	Message string `json:"message"`
}

// Table is a tabular block of data attached to a section.
type Table struct {
	Title     string     `json:"title,omitempty"`
	Headers   []string   `json:"headers"`
	Rows      [][]string `json:"rows"`
	RowLevels []Level    `json:"rowLevels,omitempty"`
	Truncated int        `json:"truncated,omitempty"`
}

// AddRow appends a row; the level drives its color.
func (t *Table) AddRow(l Level, cells ...string) {
	t.Rows = append(t.Rows, cells)
	t.RowLevels = append(t.RowLevels, l)
}

// Limit keeps at most n rows and records how many were dropped.
func (t *Table) Limit(n int) {
	if n <= 0 || len(t.Rows) <= n {
		return
	}
	t.Truncated = len(t.Rows) - n
	t.Rows = t.Rows[:n]
	t.RowLevels = t.RowLevels[:n]
}

// Section is the outcome of one check or operation.
type Section struct {
	Name     string    `json:"name"`
	Title    string    `json:"title"`
	Status   Level     `json:"status"`
	Summary  string    `json:"summary,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
	Tables   []*Table  `json:"tables,omitempty"`
}

// NewSection creates a passing section.
func NewSection(name, title string) *Section {
	return &Section{Name: name, Title: title, Status: Pass}
}

// Add records a finding and escalates the section status. Informational
// findings never change the status.
func (s *Section) Add(l Level, format string, args ...any) {
	s.Findings = append(s.Findings, Finding{Level: l, Message: fmt.Sprintf(format, args...)})
	s.Escalate(l)
}

// AddRule records a finding attached to a rule ID.
func (s *Section) AddRule(rule string, l Level, msg string) {
	s.Findings = append(s.Findings, Finding{Level: l, Rule: rule, Message: msg})
	s.Escalate(l)
}

// Escalate raises the section status (Info is ignored).
func (s *Section) Escalate(l Level) {
	if l != Info {
		s.Status = Worse(s.Status, l)
	}
}

// Fail marks the section as errored: the check itself could not run.
func (s *Section) Fail(err error, hint string) *Section {
	msg := err.Error()
	if hint != "" {
		msg += " — " + hint
	}
	s.Add(Error, "%s", msg)
	return s
}

// Skipped marks the section as not applicable.
func (s *Section) Skipped(reason string) *Section {
	s.Add(Skip, "%s", reason)
	return s
}

// NewTable attaches a new table to the section.
func (s *Section) NewTable(title string, headers ...string) *Table {
	t := &Table{Title: title, Headers: headers}
	s.Tables = append(s.Tables, t)
	return t
}

// Report is the full output of a command.
type Report struct {
	Context  string            `json:"context,omitempty"`
	Server   string            `json:"server,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
	Scenario *ScenarioRun      `json:"scenario,omitempty"`
	Sections []*Section        `json:"sections"`
	Status   Level             `json:"status"`
}

// Scenario outcomes.
const (
	OutcomeCompleted = "completed" // every step ran
	OutcomeStopped   = "stopped"   // a gate stopped the run (critical or failed step)
	OutcomeDeclined  = "declined"  // the operator answered no to a confirmation
	OutcomeCanceled  = "canceled"  // the run was interrupted (signal, timeout)
	OutcomePlanned   = "planned"   // dry run: nothing was executed
)

// ScenarioRun records the execution of a scenario: who ran it, when, and
// what each step did. Steps point to their sections by index.
type ScenarioRun struct {
	Name       string     `json:"name"`
	Source     string     `json:"source"`
	User       string     `json:"user,omitempty"`
	Tool       string     `json:"tool,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt time.Time  `json:"finishedAt"`
	Outcome    string     `json:"outcome"`
	Reason     string     `json:"reason,omitempty"`
	Steps      []*StepRun `json:"steps"`
}

// StepRun is the result of one scenario step.
type StepRun struct {
	Index       int    `json:"index"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Changes     bool   `json:"changesCluster"`
	Status      Level  `json:"status"`
	// Ran is false for the steps after the one where the scenario stopped.
	Ran      bool  `json:"ran"`
	Sections []int `json:"sections,omitempty"`
}

// Compute sets the overall status from the sections.
func (r *Report) Compute() {
	r.Status = Pass
	for _, s := range r.Sections {
		r.Status = Worse(r.Status, s.Status)
	}
}

// JSON writes the report as indented JSON.
func JSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// YAML writes the report as YAML.
func YAML(w io.Writer, r *Report) error {
	b, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ---------------------------------------------------------------- text

// Printer renders reports as human-readable text.
type Printer struct {
	W       io.Writer
	Color   bool
	Verbose bool
}

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cBlue   = "\033[34m"
	cMag    = "\033[35m"
	cCyan   = "\033[36m"
)

func (p *Printer) paint(code, s string) string {
	if !p.Color || code == "" {
		return s
	}
	return code + s + cReset
}

func levelColor(l Level) string {
	switch l {
	case Pass:
		return cGreen
	case Info:
		return cBlue
	case Skip:
		return cDim
	case Warn:
		return cYellow
	case Crit:
		return cRed
	case Error:
		return cMag
	}
	return ""
}

func (p *Printer) badge(l Level) string {
	return p.paint(levelColor(l)+cBold, fmt.Sprintf("[%-5s]", l.String()))
}

// Header prints the run banner.
func (p *Printer) Header(r *Report, title string) {
	fmt.Fprintln(p.W, p.paint(cBold, title))
	fmt.Fprintf(p.W, "  Context  : %s\n  Server   : %s\n", r.Context, r.Server)
	labels := [][2]string{{"version", "Version"}, {"channel", "Channel"}, {"topology", "Topology"}, {"platform", "Platform"}, {"config", "Config"}}
	for _, kv := range labels {
		if v, ok := r.Meta[kv[0]]; ok && v != "" {
			fmt.Fprintf(p.W, "  %-9s: %s\n", kv[1], v)
		}
	}
	fmt.Fprintln(p.W)
}

// Section prints one section.
func (p *Printer) Section(s *Section) {
	fmt.Fprintln(p.W, p.paint(cDim, strings.Repeat("─", 78)))
	fmt.Fprintf(p.W, "%s %s\n", p.badge(s.Status), p.paint(cBold, s.Title))
	if s.Summary != "" {
		fmt.Fprintf(p.W, "        %s\n", s.Summary)
	}
	for _, f := range s.Findings {
		if f.Level == Pass && !p.Verbose {
			continue
		}
		fmt.Fprintf(p.W, "   %s %s\n", p.paint(levelColor(f.Level), bullet(f.Level)), f.Message)
	}
	for _, t := range s.Tables {
		if len(t.Rows) == 0 {
			continue
		}
		fmt.Fprintln(p.W)
		p.table(t)
	}
	fmt.Fprintln(p.W)
}

func bullet(l Level) string {
	switch l {
	case Crit, Error:
		return "✗"
	case Warn:
		return "!"
	case Pass:
		return "✓"
	case Skip:
		return "-"
	}
	return "•"
}

func width(s string) int { return utf8.RuneCountInString(s) }

// maxColumnWidth caps very wide columns (messages) to keep output readable.
const maxColumnWidth = 70

func (p *Printer) table(t *Table) {
	if t.Title != "" {
		fmt.Fprintf(p.W, "   %s\n", p.paint(cCyan, t.Title))
	}
	w := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		w[i] = width(h)
	}
	for _, r := range t.Rows {
		for i := 0; i < len(r) && i < len(w); i++ {
			if n := width(r[i]); n > w[i] {
				w[i] = n
			}
		}
	}
	for i := range w {
		if w[i] > maxColumnWidth {
			w[i] = maxColumnWidth
		}
	}
	render := func(cells []string, color string, bold bool) {
		var b strings.Builder
		b.WriteString("   ")
		for i := range w {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			if width(c) > w[i] {
				c = string([]rune(c)[:w[i]-1]) + "…"
			}
			cell := c + strings.Repeat(" ", w[i]-width(c))
			if bold {
				cell = p.paint(cBold, cell)
			} else if color != "" {
				cell = p.paint(color, cell)
			}
			b.WriteString(cell)
			if i < len(w)-1 {
				b.WriteString("  ")
			}
		}
		fmt.Fprintln(p.W, strings.TrimRight(b.String(), " "))
	}
	render(t.Headers, "", true)
	for i, r := range t.Rows {
		color := ""
		if i < len(t.RowLevels) {
			switch t.RowLevels[i] {
			case Crit, Error, Warn, Skip:
				color = levelColor(t.RowLevels[i])
			}
		}
		render(r, color, false)
	}
	if t.Truncated > 0 {
		fmt.Fprintf(p.W, "   %s\n", p.paint(cDim, fmt.Sprintf("… %d more row(s) hidden (--max-rows)", t.Truncated)))
	}
}

// ScenarioPlan prints the steps of a scenario before it runs.
func (p *Printer) ScenarioPlan(sc *ScenarioRun) {
	fmt.Fprintln(p.W, p.paint(cBold, "Scenario "+sc.Name)+p.paint(cDim, "  ("+sc.Source+")"))
	for _, st := range sc.Steps {
		mark := "  read-only"
		if st.Changes {
			mark = p.paint(cYellow, "  changes the cluster")
		}
		fmt.Fprintf(p.W, "  %d. %s%s\n", st.Index, st.Description, mark)
	}
	fmt.Fprintln(p.W)
}

// StepHeader announces a scenario step.
func (p *Printer) StepHeader(st *StepRun, total int) {
	fmt.Fprintln(p.W, p.paint(cBold, fmt.Sprintf("▶ Step %d/%d — %s", st.Index, total, st.Description)))
	fmt.Fprintln(p.W)
}

// ScenarioSummary prints the final recap of a scenario.
func (p *Printer) ScenarioSummary(sc *ScenarioRun) {
	fmt.Fprintln(p.W, p.paint(cDim, strings.Repeat("═", 78)))
	fmt.Fprintln(p.W, p.paint(cBold, "Scenario "+sc.Name))
	for _, st := range sc.Steps {
		if !st.Ran {
			fmt.Fprintf(p.W, "  %s %d. %s\n", p.paint(cDim, "[ --- ]"), st.Index, p.paint(cDim, st.Description+" (not run)"))
			continue
		}
		fmt.Fprintf(p.W, "  %s %d. %s\n", p.badge(st.Status), st.Index, st.Description)
	}
	fmt.Fprintln(p.W)
	verdict, lvl := "", Pass
	switch sc.Outcome {
	case OutcomeCompleted:
		verdict = "Scenario completed."
	case OutcomePlanned:
		verdict, lvl = "Dry run: nothing was executed.", Info
	case OutcomeDeclined:
		verdict, lvl = "Scenario stopped at your request.", Warn
	case OutcomeCanceled:
		verdict, lvl = "Scenario interrupted.", Error
	default:
		verdict, lvl = "Scenario stopped.", Crit
	}
	if sc.Reason != "" {
		verdict += " " + sc.Reason
	}
	fmt.Fprintf(p.W, "%s %s\n", p.badge(lvl), p.paint(levelColor(lvl)+cBold, verdict))
}

// Summary prints the final recap.
func (p *Printer) Summary(r *Report) {
	fmt.Fprintln(p.W, p.paint(cDim, strings.Repeat("═", 78)))
	fmt.Fprintln(p.W, p.paint(cBold, "Summary"))
	for _, s := range r.Sections {
		fmt.Fprintf(p.W, "  %s %s\n", p.badge(s.Status), s.Title)
	}
	fmt.Fprintln(p.W)
	verdict := map[Level]string{
		Pass:  "No blocking issue found.",
		Info:  "No blocking issue found.",
		Skip:  "No blocking issue found (some checks were skipped).",
		Warn:  "Warnings to review before starting the upgrade.",
		Crit:  "CRITICAL issues: do not start the upgrade as is.",
		Error: "Some checks could not run: the result is incomplete.",
	}[r.Status]
	fmt.Fprintf(p.W, "%s %s\n", p.badge(r.Status), p.paint(levelColor(r.Status)+cBold, verdict))
}
