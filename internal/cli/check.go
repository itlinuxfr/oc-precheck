// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/itlinuxfr/oc-precheck/internal/checks"
	"github.com/itlinuxfr/oc-precheck/internal/report"
)

type checkOptions struct {
	only, skip, pdbIgnore []string
	listChecks, force     bool
	failOn                string
	etcdDBSize            string

	silence         bool
	silenceDuration time.Duration
	silenceComment  string
	silenceAuthor   string
	silenceMatchers []string

	banner       checks.BannerOptions
	removeBanner bool

	defrag bool
	do     checks.DefragOptions
	act    actionFlags
}

func newCheckCommand(g *globals) *cobra.Command {
	co := &checkOptions{}
	o := g.o
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Run the pre-upgrade checks (default command)",
		Long: `Run the pre-upgrade checks. All checks are read-only.

The optional actions (--silence, --banner, --remove-banner, --defrag) only run
when requested, after confirmation (or --yes), and are refused when a check is
CRITICAL unless --force is given.

Exit codes: 0 OK, 1 warnings (with --fail-on warn), 2 critical, 3 usage or
configuration error, 4 execution error.`,
		Example: g.example(
			"$CMD",
			"$CMD check --checks etcd,mcp,pdb --verbose",
			"$CMD --config precheck.yaml --target-version 4.18.24",
			"$CMD --group-by-label topology.kubernetes.io/zone -o json > precheck.json",
			`$CMD --silence --banner "Upgrade to 4.18 in progress" --defrag --dry-run`,
		),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runCheck(cmd, g, co) },
	}
	f := cmd.Flags()
	f.StringSliceVar(&co.only, "checks", nil, "Checks to run (default: all), see --list-checks")
	f.StringSliceVar(&co.skip, "skip", nil, "Checks to skip")
	f.BoolVar(&co.listChecks, "list-checks", false, "List the available checks and exit")
	f.StringVar(&co.failOn, "fail-on", "crit", "Level giving a non-zero exit code: warn or crit")

	f.StringSliceVar(&o.AlertSeverities, "alert-severities", o.AlertSeverities, "Alert severities to report")
	f.StringSliceVar(&o.IgnoreAlerts, "ignore-alerts", o.IgnoreAlerts, "Alerts to ignore (alertname)")
	f.StringVar(&o.AlertmanagerURL, "alertmanager-url", o.AlertmanagerURL, "Alertmanager URL as seen from its pod")
	f.StringVar(&o.MonitoringNS, "monitoring-namespace", o.MonitoringNS, "Namespace of the platform monitoring stack")
	f.StringVar(&o.EtcdNamespace, "etcd-namespace", o.EtcdNamespace, "Namespace of the etcd pods")
	f.Float64Var(&o.EtcdFragThreshold, "etcd-frag-threshold", o.EtcdFragThreshold, "etcd fragmentation (%) above which a defragmentation is recommended")
	f.StringVar(&co.etcdDBSize, "etcd-db-size-warn", "6Gi", "etcd database size above which to warn")
	f.IntVar(&o.EtcdExpectedMembers, "etcd-expected-members", o.EtcdExpectedMembers, "Expected number of etcd members")
	f.StringVar(&o.TargetVersion, "target-version", "", "Target version (e.g. 4.18.24), validated against the update graph")
	f.StringVar(&o.ZoneLabel, "zone-label", o.ZoneLabel, "Node label holding the zone / room")
	f.StringSliceVar(&o.GroupByLabels, "group-by-label", nil, "Node labels used to group the pod count (repeatable)")
	f.StringVar(&o.NodeSelector, "node-selector", "", "Restrict the nodes check to a label selector")
	f.Float64Var(&o.NodePodCapacityPct, "node-pod-capacity", o.NodePodCapacityPct, "Pod capacity usage (%) per node above which to warn")
	f.DurationVar(&o.PodPendingAge, "pod-pending-age", o.PodPendingAge, "Report Pending pods older than this")
	f.DurationVar(&o.PodNotReadyAge, "pod-notready-age", o.PodNotReadyAge, "Report Running pods not ready for longer than this (0 = immediately)")
	f.DurationVar(&o.PodTerminatingAge, "pod-terminating-age", o.PodTerminatingAge, "Report pods Terminating for longer than this")
	f.Int32Var(&o.PodRestartWarn, "pod-restart-warn", o.PodRestartWarn, "Restart count reported as information")
	f.Float64Var(&o.QuotaThreshold, "quota-threshold", o.QuotaThreshold, "Quota usage (%) above which to warn")
	f.DurationVar(&o.JobLongDuration, "job-long-duration", o.JobLongDuration, "Warn about Jobs running longer than this")
	f.StringSliceVar(&o.HCPFilter, "hcp-filter", nil, "Restrict to HostedClusters (globs on name or namespace/name)")
	f.StringSliceVar(&co.pdbIgnore, "pdb-ignore-namespaces", nil, "Namespaces whose PDBs are ignored (exclusion shortcut)")

	f.BoolVar(&co.silence, "silence", false, "ACTION: create an Alertmanager silence for the maintenance window")
	f.DurationVar(&co.silenceDuration, "silence-duration", 3*time.Hour, "Silence duration")
	f.StringVar(&co.silenceComment, "silence-comment", "OpenShift upgrade in progress (oc-precheck)", "Silence comment")
	f.StringVar(&co.silenceAuthor, "silence-author", "", "Silence author (default: current OS user)")
	f.StringArrayVar(&co.silenceMatchers, "silence-matcher", nil, "amtool matcher (repeatable), default alertname=~.+ (everything)")
	f.StringVar(&co.banner.Text, "banner", "", "ACTION: show this text as a web console banner")
	f.StringVar(&co.banner.Name, "banner-name", checks.DefaultBannerName, "ConsoleNotification name")
	f.StringVar(&co.banner.Location, "banner-location", "BannerTop", "BannerTop, BannerBottom or BannerTopBottom")
	f.StringVar(&co.banner.Color, "banner-color", "#fff", "Banner text color")
	f.StringVar(&co.banner.Background, "banner-bg", "#c9190b", "Banner background color")
	f.StringVar(&co.banner.LinkURL, "banner-link", "", "Optional link (e.g. change ticket)")
	f.StringVar(&co.banner.LinkText, "banner-link-text", "", "Link text")
	f.BoolVar(&co.removeBanner, "remove-banner", false, "ACTION: remove the banners created by oc-precheck (end of maintenance)")
	f.BoolVar(&co.defrag, "defrag", false, "ACTION: defragment etcd, leader last")
	f.Float64Var(&co.do.MinFragmentation, "defrag-min-frag", 0, "Only defragment members above this fragmentation (%)")
	f.DurationVar(&co.do.Pause, "defrag-pause", 15*time.Second, "Pause between two members")
	f.DurationVar(&co.do.CommandTimeout, "defrag-command-timeout", 60*time.Second, "Timeout of etcdctl defrag")
	f.BoolVar(&co.do.DisarmAlarms, "defrag-disarm-alarms", false, "Disarm etcd alarms (NOSPACE) after defragmentation")
	f.BoolVar(&co.force, "force", false, "Run the actions even when checks are CRITICAL")
	co.act.add(cmd)
	return cmd
}

func runCheck(cmd *cobra.Command, g *globals, co *checkOptions) error {
	o := g.o
	fl := cmd.Flags()
	if !fl.Changed("fail-on") && g.cfg != nil && g.cfg.Settings.FailOn != "" {
		co.failOn = g.cfg.Settings.FailOn
	}
	if co.failOn != "warn" && co.failOn != "crit" {
		return usageError("--fail-on must be warn or crit")
	}
	if fl.Changed("etcd-db-size-warn") || g.cfg == nil || g.cfg.Settings.EtcdDBSizeWarn == "" {
		q, err := resource.ParseQuantity(co.etcdDBSize)
		if err != nil {
			return usageError("invalid --etcd-db-size-warn: %w", err)
		}
		o.EtcdDBSizeWarn = q.Value()
	}
	o.ExcludeNamespaces("pdb.*", co.pdbIgnore, "--pdb-ignore-namespaces")
	if g.cfg != nil {
		if !fl.Changed("checks") && len(g.cfg.Settings.Checks) > 0 {
			co.only = g.cfg.Settings.Checks
		}
		if !fl.Changed("skip") && len(g.cfg.Settings.Skip) > 0 {
			co.skip = g.cfg.Settings.Skip
		}
	}

	all := checks.All()
	if co.listChecks {
		for _, c := range all {
			fmt.Fprintf(g.streams.Out, "  %-17s %s\n", c.Name, c.Description)
		}
		return nil
	}
	selected, err := selectChecks(all, co.only, co.skip, len(o.CustomRules) > 0)
	if err != nil {
		return err
	}

	ss, err := g.connect()
	if err != nil {
		return err
	}
	defer ss.cancel()
	ss.header("oc-precheck — OpenShift pre-upgrade checks")
	if g.text() {
		fmt.Fprintf(g.streams.ErrOut, "Running %d check(s)…\n\n", len(selected))
	}

	results := make([]*report.Section, len(selected))
	var wg sync.WaitGroup
	for i, c := range selected {
		wg.Add(1)
		go func(i int, c checks.Check) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[i] = report.NewSection(c.Name, c.Description).Fail(fmt.Errorf("internal error: %v", r), "please report it")
				}
			}()
			results[i] = c.Run(ss.ctx, ss.env, o)
		}(i, c)
	}
	wg.Wait()
	for _, s := range results {
		ss.add(s)
	}

	if !co.silence && !co.defrag && co.banner.Text == "" && !co.removeBanner {
		return ss.finish(co.failOn)
	}
	ss.rep.Compute()
	if ss.rep.Status >= report.Crit && !co.force && !co.act.dryRun && !co.removeBanner {
		s := report.NewSection("actions", "Actions")
		s.Add(report.Crit, "Actions canceled: some checks are CRITICAL (--force to override)")
		ss.add(s)
		return ss.finish(co.failOn)
	}
	var plan []string
	if co.silence {
		plan = append(plan, fmt.Sprintf("create a %s Alertmanager silence", co.silenceDuration))
	}
	if co.banner.Text != "" {
		plan = append(plan, fmt.Sprintf("show the console banner %q", co.banner.Text))
	}
	if co.removeBanner {
		plan = append(plan, "remove the console banners created by oc-precheck")
	}
	if co.defrag {
		plan = append(plan, "defragment etcd member by member (leader last)")
	}
	if !co.act.dryRun && !co.act.yes && !g.confirm(fmt.Sprintf("Cluster %s (%s): %s. Continue?", ss.kc.Context, ss.kc.Server, strings.Join(plan, ", then "))) {
		fmt.Fprintln(g.streams.ErrOut, "Actions canceled.")
		return ss.finish(co.failOn)
	}
	if co.silence {
		ss.add(checks.ActionSilence(ss.ctx, ss.env, o, checks.SilenceOptions{
			Duration: co.silenceDuration, Author: authorOrDefault(co.silenceAuthor), Comment: co.silenceComment,
			Matchers: co.silenceMatchers, DryRun: co.act.dryRun,
		}))
	}
	if co.banner.Text != "" {
		co.banner.DryRun = co.act.dryRun
		ss.add(checks.ActionBanner(ss.ctx, ss.env, co.banner))
	}
	if co.removeBanner {
		ss.add(checks.ActionRemoveBanner(ss.ctx, ss.env, nil, true, co.act.dryRun))
	}
	if co.defrag {
		co.do.DryRun, co.do.Log = co.act.dryRun, g.streams.ErrOut
		ss.add(checks.ActionDefrag(ss.ctx, ss.env, o, co.do))
	}
	return ss.finish(co.failOn)
}

func selectChecks(all []checks.Check, only, skip []string, hasCustom bool) ([]checks.Check, error) {
	known := map[string]bool{}
	var names []string
	for _, c := range all {
		known[c.Name] = true
		names = append(names, c.Name)
	}
	sort.Strings(names)
	for _, n := range append(append([]string{}, only...), skip...) {
		if !known[n] {
			return nil, usageError("unknown check %q (available: %s)", n, strings.Join(names, ", "))
		}
	}
	in := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	var out []checks.Check
	for _, c := range all {
		switch {
		case len(only) > 0 && !in(only, c.Name):
		case len(only) == 0 && c.Name == "custom" && !hasCustom:
		case in(skip, c.Name):
		default:
			out = append(out, c)
		}
	}
	return out, nil
}
