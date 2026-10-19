// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

// Package cli implements the command-line interface (cobra commands).
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/itlinuxfr/oc-precheck/internal/checks"
	"github.com/itlinuxfr/oc-precheck/internal/kube"
	"github.com/itlinuxfr/oc-precheck/internal/report"
	"github.com/itlinuxfr/oc-precheck/internal/version"
)

// Exit codes.
const (
	CodeOK    = 0
	CodeWarn  = 1
	CodeCrit  = 2
	CodeUsage = 3
	CodeError = 4
)

// ExitError carries a process exit code through cobra.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

func usageError(format string, a ...any) error {
	return &ExitError{Code: CodeUsage, Err: fmt.Errorf(format, a...)}
}

// IOStreams are the standard streams, injectable for tests.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// globals holds the persistent flags and the shared state of a run.
type globals struct {
	streams    IOStreams
	kube       *kube.Flags
	output     string
	noColor    bool
	configPath string
	timeout    time.Duration
	o          *checks.Options
	cfg        *checks.Config
	name       string // command name as displayed in help (oc precheck…)
}

// commandName returns how the user invoked the tool: "oc precheck" or
// "kubectl precheck" when run as a plugin, the binary name otherwise.
func commandName(argv0 string) string {
	// Handle both separators: the binary may be invoked from Windows.
	base := strings.TrimSuffix(path.Base(strings.ReplaceAll(argv0, `\`, "/")), ".exe")
	for _, p := range []string{"oc-", "kubectl-"} {
		if strings.HasPrefix(base, p) {
			return strings.TrimSuffix(p, "-") + " " + strings.ReplaceAll(strings.TrimPrefix(base, p), "_", "-")
		}
	}
	if base == "" || base == "." {
		return "oc-precheck"
	}
	return base
}

// NewRootCommand builds the command tree.
func NewRootCommand(streams IOStreams, argv0 string) *cobra.Command {
	g := &globals{streams: streams, kube: kube.NewFlags(), o: checks.DefaultOptions(), name: commandName(argv0)}
	root := &cobra.Command{
		Use:   "oc-precheck",
		Short: "Pre-upgrade checks and maintenance operations for OpenShift 4",
		Long: `oc-precheck verifies that an OpenShift 4 cluster is ready to be upgraded and
helps run the maintenance around it: Alertmanager silences, web console banner,
etcd defragmentation, drain of a whole room / zone, live node view, and a global
status of HostedClusters from a HyperShift hub.

Running it without a command runs "check". It works as an oc / kubectl plugin
when installed as "oc-precheck" or "kubectl-precheck" in the PATH.`,
		Version:       version.Get().Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Annotations:   map[string]string{cobra.CommandDisplayNameAnnotation: g.name},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return g.loadConfig(cmd)
		},
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.ErrOut)
	root.SetVersionTemplate("{{.Version}}\n")

	pf := root.PersistentFlags()
	g.kube.AddFlags(pf)
	pf.StringVar(&g.configPath, "config", "", "Configuration file (rules, thresholds, exclusions); defaults to $OCP_PRECHECK_CONFIG")
	pf.StringVarP(&g.output, "output", "o", "text", "Output format: text, json or yaml")
	pf.BoolVar(&g.noColor, "no-color", false, "Disable colors (also honours NO_COLOR)")
	pf.BoolVar(&g.o.Verbose, "verbose", false, "Also show healthy items")
	pf.IntVar(&g.o.MaxRows, "max-rows", g.o.MaxRows, "Maximum rows per table (0 = unlimited)")
	pf.DurationVar(&g.timeout, "timeout", 2*time.Hour, "Global timeout of the command")

	root.AddGroup(&cobra.Group{ID: "checks", Title: "Checks:"}, &cobra.Group{ID: "ops", Title: "Operations:"}, &cobra.Group{ID: "config", Title: "Configuration:"})
	for _, c := range []*cobra.Command{newCheckCommand(g), newWatchCommand(g)} {
		c.GroupID = "checks"
		root.AddCommand(c)
	}
	for _, c := range []*cobra.Command{newDrainCommand(g), newUncordonCommand(g), newBannerCommand(g), newSilenceCommand(g), newDefragCommand(g)} {
		c.GroupID = "ops"
		root.AddCommand(c)
	}
	for _, c := range []*cobra.Command{newRulesCommand(g), newConfigCommand(g)} {
		c.GroupID = "config"
		root.AddCommand(c)
	}
	root.AddCommand(newVersionCommand(g), newGenDocsCommand())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	streams := IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
	root := NewRootCommand(streams, os.Args[0])
	root.SetArgs(defaultToCheck(root, os.Args[1:]))
	err := root.Execute()
	if err == nil {
		return CodeOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.Err != nil {
			fmt.Fprintln(streams.ErrOut, "Error:", ee.Err)
		}
		return ee.Code
	}
	fmt.Fprintln(streams.ErrOut, "Error:", err)
	return CodeUsage
}

// defaultToCheck inserts "check" when no sub-command is given, so that
// `oc precheck --target-version 4.18.24` runs the checks.
func defaultToCheck(root *cobra.Command, args []string) []string {
	names := map[string]bool{"help": true, "completion": true, "__complete": true, "__completeNoDesc": true}
	for _, c := range root.Commands() {
		names[c.Name()] = true
		for _, a := range c.Aliases {
			names[a] = true
		}
	}
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "--version" || names[a] {
			return args // explicit sub-command or help
		}
	}
	return append([]string{"check"}, args...)
}

// loadConfig applies the configuration file; explicit flags win.
func (g *globals) loadConfig(cmd *cobra.Command) error {
	switch g.output {
	case "text", "json", "yaml":
	default:
		return usageError("--output must be text, json or yaml")
	}
	if g.configPath == "" {
		g.configPath = os.Getenv("OCP_PRECHECK_CONFIG")
	}
	if g.configPath == "" {
		return nil
	}
	cfg, err := checks.LoadConfig(g.configPath)
	if err == nil {
		err = cfg.Apply(g.o, cmd.Flags().Changed)
	}
	if err != nil {
		return usageError("invalid configuration: %w", err)
	}
	g.cfg = cfg
	return nil
}

// ------------------------------------------------------------- session

type session struct {
	ctx    context.Context
	cancel func()
	kc     *kube.Client
	env    *checks.Env
	rep    *report.Report
	pr     *report.Printer
	g      *globals
}

func (g *globals) connect() (*session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	release := func() { stop(); cancel() }
	kc, err := g.kube.New()
	if err != nil {
		release()
		return nil, &ExitError{Code: CodeError, Err: err}
	}
	if _, err := kc.CS.Discovery().ServerVersion(); err != nil {
		release()
		return nil, &ExitError{Code: CodeError, Err: fmt.Errorf("cannot reach the API server %s: %w (logged in? token expired?)", kc.Server, err)}
	}
	env := &checks.Env{CS: kc.CS, Dyn: kc.Dyn, Exec: kc}
	rep := &report.Report{Context: kc.Context, Server: kc.Server, Meta: checks.Discover(ctx, env)}
	if g.configPath != "" {
		rep.Meta["config"] = g.configPath
	}
	return &session{
		ctx: ctx, cancel: release, kc: kc, env: env, rep: rep, g: g,
		pr: &report.Printer{W: g.streams.Out, Color: g.color(), Verbose: g.o.Verbose},
	}, nil
}

func (g *globals) text() bool { return g.output == "text" }

func (g *globals) color() bool {
	if g.noColor || !g.text() || os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := g.streams.Out.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (s *session) header(title string) {
	if s.g.text() {
		s.pr.Header(s.rep, title)
	}
}

func (s *session) add(sec *report.Section) {
	s.rep.Sections = append(s.rep.Sections, sec)
	if s.g.text() {
		s.pr.Section(sec)
	}
}

// finish renders the report and converts its status into an exit code.
func (s *session) finish(failOn string) error {
	s.rep.Compute()
	var err error
	switch s.g.output {
	case "json":
		err = report.JSON(s.g.streams.Out, s.rep)
	case "yaml":
		err = report.YAML(s.g.streams.Out, s.rep)
	default:
		s.pr.Summary(s.rep)
	}
	if err != nil {
		return &ExitError{Code: CodeError, Err: err}
	}
	switch {
	case s.rep.Status == report.Error:
		return &ExitError{Code: CodeError}
	case s.rep.Status == report.Crit:
		return &ExitError{Code: CodeCrit}
	case s.rep.Status == report.Warn && failOn == "warn":
		return &ExitError{Code: CodeWarn}
	}
	return nil
}

// confirm asks for an interactive "yes".
func (g *globals) confirm(question string) bool {
	f, ok := g.streams.In.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		fmt.Fprintln(g.streams.ErrOut, "Non-interactive input: use --yes to confirm.")
		return false
	}
	fmt.Fprintf(g.streams.ErrOut, "\n%s [yes/NO]: ", question)
	line, _ := bufio.NewReader(g.streams.In).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// actionFlags are shared by the commands that change the cluster.
type actionFlags struct {
	dryRun, yes bool
}

func (a *actionFlags) add(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&a.dryRun, "dry-run", false, "Show what would be done without changing anything")
	cmd.Flags().BoolVarP(&a.yes, "yes", "y", false, "Do not ask for confirmation")
}

// authorOrDefault returns the given silence author or the current OS user.
func authorOrDefault(author string) string {
	if author != "" {
		return author
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username + " (oc-precheck)"
	}
	return "oc-precheck"
}

// example formats examples with the displayed command name.
func (g *globals) example(lines ...string) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, "  "+strings.ReplaceAll(l, "$CMD", g.name))
	}
	return strings.Join(out, "\n")
}
