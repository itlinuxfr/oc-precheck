// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
	"sigs.k8s.io/yaml"

	"github.com/itlinuxfr/oc-precheck/internal/checks"
	"github.com/itlinuxfr/oc-precheck/internal/report"
	"github.com/itlinuxfr/oc-precheck/internal/version"
)

// -------------------------------------------------------------- rules

type ruleRow struct {
	ID          string       `json:"id"`
	Check       string       `json:"check"`
	Default     string       `json:"defaultSeverity"`
	Effective   string       `json:"effectiveSeverity"`
	Description string       `json:"description"`
	Level       report.Level `json:"-"`
}

func ruleRows(o *checks.Options, filter string) []ruleRow {
	cat := append([]checks.RuleInfo{}, checks.Catalog...)
	for _, r := range o.CustomRules {
		cat = append(cat, checks.RuleInfo{ID: r.ID, Check: "custom", Level: r.Level, Description: firstNonEmptyStr(r.Description, r.Expr)})
	}
	sort.SliceStable(cat, func(i, j int) bool { return cat[i].Check < cat[j].Check })
	var rows []ruleRow
	for _, r := range cat {
		if filter != "" && r.Check != filter {
			continue
		}
		eff, on := o.Policy.Effective(r.ID)
		e := checks.LevelName(eff)
		if !on {
			e = "off"
		}
		rows = append(rows, ruleRow{ID: r.ID, Check: r.Check, Default: checks.LevelName(r.Level), Effective: e, Description: r.Description, Level: r.Level})
	}
	return rows
}

func newRulesCommand(g *globals) *cobra.Command {
	var check string
	var markdown bool
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "List the rules with their default and effective severity",
		Long: `List every rule (built-in and custom) with its default severity and, when
--config is given, its effective severity. Rule IDs are what the configuration
file uses in "severities" and "exclusions".`,
		Example: g.example("$CMD rules", "$CMD rules --check pods", "$CMD rules --config precheck.yaml"),
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			rows := ruleRows(g.o, check)
			switch {
			case markdown:
				fmt.Fprint(g.streams.Out, rulesMarkdown(rows))
				return nil
			case g.output == "json":
				enc := json.NewEncoder(g.streams.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			case g.output == "yaml":
				b, err := yaml.Marshal(rows)
				if err != nil {
					return err
				}
				_, err = g.streams.Out.Write(b)
				return err
			}
			s := report.NewSection("rules", "Rules")
			t := s.NewTable("", "Rule", "Check", "Default", "Effective", "Description")
			for _, r := range rows {
				lvl := report.Pass
				if r.Effective != checks.LevelName(r.Level) {
					lvl = report.Info
				}
				t.AddRow(lvl, r.ID, r.Check, checks.LevelName(r.Level), r.Effective, r.Description)
			}
			s.Summary = fmt.Sprintf("%d rule(s)", len(rows))
			(&report.Printer{W: g.streams.Out, Color: g.color()}).Section(s)
			return nil
		},
	}
	cmd.Flags().StringVar(&check, "check", "", "Only list the rules of this check")
	cmd.Flags().BoolVar(&markdown, "markdown", false, "Render as a Markdown table")
	return cmd
}

func rulesMarkdown(rows []ruleRow) string {
	var b strings.Builder
	b.WriteString("| Rule | Check | Default severity | Description |\n|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", r.ID, r.Check, checks.LevelName(r.Level), strings.ReplaceAll(r.Description, "|", "\\|"))
	}
	return b.String()
}

// ------------------------------------------------------------- config

func newConfigCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Generate or validate a configuration file",
	}
	var outFile string
	def := &cobra.Command{
		Use:   "default",
		Short: "Print the default configuration (every setting and rule with its default value)",
		Long: `Print the default configuration as YAML: every setting with its default value,
every rule with its default severity, and commented examples of exclusions and
custom CEL rules. It is generated from the code and always up to date.`,
		Example: g.example("$CMD config default > precheck.yaml", "$CMD config default --output-file precheck.yaml"),
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			content := checks.DefaultConfigYAML()
			if outFile == "" {
				_, err := fmt.Fprint(g.streams.Out, content)
				return err
			}
			if _, err := os.Stat(outFile); err == nil {
				return usageError("%s already exists", outFile)
			}
			if err := os.WriteFile(outFile, []byte(content), 0o644); err != nil {
				return &ExitError{Code: CodeError, Err: err}
			}
			fmt.Fprintf(g.streams.ErrOut, "Default configuration written to %s\n", outFile)
			return nil
		},
	}
	def.Flags().StringVarP(&outFile, "output-file", "f", "", "Write to this file instead of stdout (never overwrites)")

	validate := &cobra.Command{
		Use:     "validate",
		Short:   "Validate a configuration file (exit code 3 when invalid)",
		Example: g.example("$CMD config validate --config precheck.yaml"),
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if g.configPath == "" {
				return usageError("--config is required")
			}
			// The file was already loaded and validated by the root command.
			p := g.o.Policy
			fmt.Fprintf(g.streams.Out, "%s is valid: %d severity override(s), %d exclusion(s), %d custom rule(s)\n",
				g.configPath, len(p.Severities), len(p.Exclusions), len(g.o.CustomRules))
			return nil
		},
	}
	cmd.AddCommand(def, validate)
	return cmd
}

// ------------------------------------------------------------ version

func newVersionCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			v := version.Get()
			switch g.output {
			case "json":
				enc := json.NewEncoder(g.streams.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(v)
			case "yaml":
				b, err := yaml.Marshal(v)
				if err != nil {
					return err
				}
				_, err = g.streams.Out.Write(b)
				return err
			}
			fmt.Fprintln(g.streams.Out, v.String())
			return nil
		},
	}
}

// ----------------------------------------------------------- gen-docs

func newGenDocsCommand() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:    "gen-docs",
		Short:  "Generate the reference documentation (CLI pages, rules, default configuration)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cliDir := filepath.Join(dir, "cli")
			if err := os.MkdirAll(cliDir, 0o755); err != nil {
				return err
			}
			root := cmd.Root()
			root.DisableAutoGenTag = true
			if err := doc.GenMarkdownTree(root, cliDir); err != nil {
				return err
			}
			rules := "# Rules\n\nGenerated by `make docs` — do not edit.\n\n" +
				"Every rule can be re-levelled (`crit`, `warn`, `info`, `off`) in the `severities` section of the\n" +
				"configuration file, and scoped out with `exclusions`. See [configuration.md](configuration.md).\n\n" +
				rulesMarkdown(ruleRows(checks.DefaultOptions(), ""))
			if err := os.WriteFile(filepath.Join(dir, "rules.md"), []byte(rules), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "default-config.yaml"), []byte(checks.DefaultConfigYAML()), 0o644)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "docs", "Output directory")
	return cmd
}

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
