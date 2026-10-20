// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/itlinuxfr/oc-upkit/internal/checks"
	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// -------------------------------------------------------------- watch

func newWatchCommand(g *globals) *cobra.Command {
	var (
		interval      time.Duration
		noPods, noHCP bool
		once          bool
		wo            checks.WatchOptions
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Live view of nodes, MachineConfigPools and HostedClusters",
		Long: `Live view during an upgrade or a maintenance: node status, cordon,
machine-config-daemon state and rendered config (current → target), pool and
cluster update progress; on a HyperShift hub, HostedClusters and NodePools.
Press Ctrl-C to quit.`,
		Example: g.example(
			"$CMD watch",
			"$CMD watch --selector topology.kubernetes.io/zone=room-a --interval 5s",
			"$CMD watch --problems",
		),
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if interval < 2*time.Second {
				interval = 2 * time.Second
			}
			wo.Pods, wo.HCP = !noPods, !noHCP
			ss, err := g.connect()
			if err != nil {
				return err
			}
			defer ss.cancel()
			out, isFile := g.streams.Out.(*os.File)
			tty := isFile && term.IsTerminal(int(out.Fd()))
			for {
				secs := checks.WatchSnapshot(ss.ctx, ss.env, g.o, wo)
				switch g.output {
				case "json", "yaml":
					ss.rep.Sections = secs
					ss.rep.Compute()
					if g.output == "json" {
						_ = report.JSON(g.streams.Out, ss.rep)
					} else {
						_ = report.YAML(g.streams.Out, ss.rep)
						fmt.Fprintln(g.streams.Out, "---")
					}
				default:
					if tty && !once {
						fmt.Fprint(g.streams.Out, "\033[H\033[2J")
					}
					fmt.Fprintf(g.streams.Out, "%s watch — %s — %s — refreshed at %s (every %s, Ctrl-C to quit)\n\n",
						g.name, ss.kc.Context, ss.rep.Meta["version"], time.Now().Format("15:04:05"), interval)
					for _, s := range secs {
						ss.pr.Section(s)
					}
				}
				if once {
					return nil
				}
				select {
				case <-ss.ctx.Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
	}
	f := cmd.Flags()
	f.DurationVar(&interval, "interval", 10*time.Second, "Refresh interval")
	f.StringVarP(&wo.Selector, "selector", "l", "", "Only show nodes matching this label selector")
	f.StringVar(&g.o.ZoneLabel, "zone-label", g.o.ZoneLabel, "Node label holding the zone / room")
	f.BoolVar(&noPods, "no-pods", false, "Do not count pods per node (lighter on large clusters)")
	f.BoolVar(&noHCP, "no-hcp", false, "Do not show HostedClusters (hub)")
	f.StringSliceVar(&g.o.HCPFilter, "hcp-filter", nil, "Restrict to HostedClusters (globs)")
	f.BoolVar(&wo.Problems, "problems", false, "Only show nodes that are not nominal")
	f.BoolVar(&once, "once", false, "Refresh once and exit")
	return cmd
}

// -------------------------------------------------------------- drain

func newDrainCommand(g *globals) *cobra.Command {
	do := checks.DrainOptions{GracePeriod: -1}
	var skipCapacity bool
	var act actionFlags
	cmd := &cobra.Command{
		Use:   "drain",
		Short: "Drain every node matching a label selector (e.g. a whole room)",
		Long: `Drain a set of nodes selected by label, typically a whole room / availability
zone. A pre-flight analysis is shown first: pods to evict, blocking pods (no
controller, emptyDir), affected PodDisruptionBudgets and remaining capacity per
zone. All target nodes are cordoned first (so that pods do not land in the same
room), then drained through the eviction API: PodDisruptionBudgets are honored
and evictions retried; DaemonSet and static pods are ignored.

The drain is refused when the selection contains control plane nodes, when
blocking pods remain or when the remaining capacity looks insufficient.`,
		Example: g.example(
			"$CMD drain --selector topology.kubernetes.io/zone=room-a --dry-run",
			"$CMD drain -l topology.kubernetes.io/zone=room-a --parallel 2 --delete-emptydir-data",
			"$CMD drain -l example.com/room=dc1-a --cordon-only",
			"$CMD uncordon -l topology.kubernetes.io/zone=room-a",
		),
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if do.Selector == "" {
				return usageError("--selector is required (e.g. --selector topology.kubernetes.io/zone=room-a)")
			}
			ss, err := g.connect()
			if err != nil {
				return err
			}
			defer ss.cancel()
			ss.header("oc-upkit — drain")
			plan := checks.PlanDrain(ss.ctx, ss.env, g.o, do)
			ss.add(plan.Section)
			switch {
			case plan.Section.Status == report.Error || len(plan.Nodes) == 0:
				return ss.finish("crit")
			case plan.Blocking > 0 && !do.CordonOnly:
				ss.add(report.NewSection("drain", "Action: drain").Fail(errors.New("blocking pods present"), "drain canceled"))
				return ss.finish("crit")
			case plan.Section.Status == report.Crit && !skipCapacity && !do.CordonOnly && !act.dryRun:
				ss.add(report.NewSection("drain", "Action: drain").Fail(errors.New("remaining capacity looks insufficient"),
					"drain canceled (--skip-capacity-check to override)"))
				return ss.finish("crit")
			}
			names := make([]string, 0, len(plan.Nodes))
			for _, n := range plan.Nodes {
				names = append(names, n.Name)
			}
			verb := "Drain"
			if do.CordonOnly {
				verb = "Cordon"
			}
			if !act.dryRun && !act.yes && !g.confirm(fmt.Sprintf("Cluster %s: %s %d node(s) [%s]?", ss.kc.Context, verb, len(names), strings.Join(names, ", "))) {
				fmt.Fprintln(g.streams.ErrOut, "Drain canceled.")
				return ss.finish("crit")
			}
			do.DryRun, do.Log = act.dryRun, g.streams.ErrOut
			ss.add(checks.ExecuteDrain(ss.ctx, ss.env, g.o, plan, do))
			return ss.finish("crit")
		},
	}
	f := cmd.Flags()
	f.StringVarP(&do.Selector, "selector", "l", "", "Label selector of the nodes to drain (required)")
	f.StringVar(&g.o.ZoneLabel, "zone-label", g.o.ZoneLabel, "Node label holding the zone / room")
	f.IntVar(&do.Parallel, "parallel", 1, "Number of nodes drained at the same time")
	f.IntVar(&do.PodParallel, "pod-parallel", 5, "Concurrent evictions per node")
	f.DurationVar(&do.Timeout, "node-timeout", 30*time.Minute, "Maximum drain duration per node")
	f.Int64Var(&do.GracePeriod, "grace-period", -1, "Pod grace period in seconds (-1 = the pod's own value)")
	f.BoolVar(&do.DeleteEmptyDir, "delete-emptydir-data", false, "Accept losing emptyDir volumes")
	f.BoolVar(&do.Force, "force", false, "Delete pods without a controller (they will not be recreated)")
	f.BoolVar(&do.IncludeControlPlane, "include-control-plane", false, "Allow control plane nodes in the selection")
	f.BoolVar(&do.CordonOnly, "cordon-only", false, "Cordon without draining")
	f.BoolVar(&skipCapacity, "skip-capacity-check", false, "Proceed even if the remaining capacity looks insufficient")
	act.add(cmd)
	return cmd
}

func newUncordonCommand(g *globals) *cobra.Command {
	var selector string
	var act actionFlags
	cmd := &cobra.Command{
		Use:     "uncordon",
		Short:   "Make the nodes matching a label selector schedulable again",
		Example: g.example("$CMD uncordon -l topology.kubernetes.io/zone=room-a"),
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if selector == "" {
				return usageError("--selector is required")
			}
			ss, err := g.connect()
			if err != nil {
				return err
			}
			defer ss.cancel()
			if !act.dryRun && !act.yes && !g.confirm(fmt.Sprintf("Cluster %s: uncordon nodes matching %q?", ss.kc.Context, selector)) {
				return nil
			}
			ss.add(checks.Uncordon(ss.ctx, ss.env, selector, act.dryRun))
			return ss.finish("crit")
		},
	}
	cmd.Flags().StringVarP(&selector, "selector", "l", "", "Label selector of the nodes (required)")
	act.add(cmd)
	return cmd
}

// ------------------------------------------------------------- banner

func newBannerCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "banner",
		Short: "Manage the web console banner (ConsoleNotification)",
		Example: g.example(
			`$CMD banner set --text "Upgrade to 4.18 in progress — CHG0012345" --link https://itsm.example.com/CHG0012345`,
			"$CMD banner list",
			"$CMD banner remove",
			"$CMD banner remove --all",
		),
	}
	bo := checks.BannerOptions{}
	var setAct actionFlags
	set := &cobra.Command{
		Use:     "set",
		Aliases: []string{"create", "add"},
		Short:   "Create or update a banner",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if bo.Text == "" {
				return usageError("--text is required")
			}
			return g.single(func(ss *session) *report.Section {
				bo.DryRun = setAct.dryRun
				return checks.ActionBanner(ss.ctx, ss.env, bo)
			})
		},
	}
	sf := set.Flags()
	sf.StringVar(&bo.Name, "name", checks.DefaultBannerName, "ConsoleNotification name")
	sf.StringVarP(&bo.Text, "text", "t", "", "Banner text (required)")
	sf.StringVar(&bo.Location, "location", "BannerTop", "BannerTop, BannerBottom or BannerTopBottom")
	sf.StringVar(&bo.Color, "color", "#fff", "Text color")
	sf.StringVar(&bo.Background, "bg", "#c9190b", "Background color (e.g. #c9190b red, #f0ab00 orange, #0066cc blue)")
	sf.StringVar(&bo.LinkURL, "link", "", "Optional link")
	sf.StringVar(&bo.LinkText, "link-text", "", "Link text")
	set.Flags().BoolVar(&setAct.dryRun, "dry-run", false, "Show what would be done without changing anything")

	var names []string
	var all, rmDry bool
	remove := &cobra.Command{
		Use:     "remove [NAME...]",
		Aliases: []string{"delete", "rm"},
		Short:   "Remove banners (default: " + checks.DefaultBannerName + ")",
		RunE: func(_ *cobra.Command, args []string) error {
			targets := append(append([]string{}, names...), args...)
			return g.single(func(ss *session) *report.Section {
				return checks.ActionRemoveBanner(ss.ctx, ss.env, targets, all, rmDry)
			})
		},
	}
	remove.Flags().StringSliceVar(&names, "name", nil, "Banner name(s) to remove")
	remove.Flags().BoolVar(&all, "all", false, "Remove every banner created by oc-upkit")
	remove.Flags().BoolVar(&rmDry, "dry-run", false, "Show what would be removed")

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the banners",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return g.single(func(ss *session) *report.Section { return checks.ListBanners(ss.ctx, ss.env) })
		},
	}
	cmd.AddCommand(set, remove, list)
	return cmd
}

// single connects, runs one operation and renders its section.
func (g *globals) single(fn func(*session) *report.Section) error {
	ss, err := g.connect()
	if err != nil {
		return err
	}
	defer ss.cancel()
	ss.add(fn(ss))
	return ss.finish("crit")
}

// ------------------------------------------------------------ silence

func newSilenceCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "silence",
		Aliases: []string{"silences"},
		Short:   "Manage Alertmanager silences (shown with their UUID)",
		Example: g.example(
			"$CMD silence list",
			`$CMD silence add --duration 4h --comment "Upgrade to 4.18 — CHG0012345"`,
			"$CMD silence add --matcher 'namespace=~openshift-.*' --matcher 'severity!=critical'",
			"$CMD silence expire 6f0c3c2e-1111-2222-3333-444455556666",
		),
	}
	cmd.PersistentFlags().StringVar(&g.o.MonitoringNS, "monitoring-namespace", g.o.MonitoringNS, "Namespace of the platform monitoring stack")

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List active silences",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return g.single(func(ss *session) *report.Section { return checks.CheckSilences(ss.ctx, ss.env, g.o) })
		},
	}
	so := checks.SilenceOptions{}
	add := &cobra.Command{
		Use:     "add",
		Aliases: []string{"create"},
		Short:   "Create a silence (default: every alert)",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			so.Author = authorOrDefault(so.Author)
			return g.single(func(ss *session) *report.Section { return checks.ActionSilence(ss.ctx, ss.env, g.o, so) })
		},
	}
	af := add.Flags()
	af.DurationVar(&so.Duration, "duration", 3*time.Hour, "Silence duration")
	af.StringVar(&so.Comment, "comment", "OpenShift maintenance (oc-upkit)", "Comment")
	af.StringVar(&so.Author, "author", "", "Author (default: current OS user)")
	af.StringArrayVar(&so.Matchers, "matcher", nil, "amtool matcher (repeatable), default alertname=~.+ (everything)")
	af.BoolVar(&so.DryRun, "dry-run", false, "Show what would be done")

	var expDry bool
	expire := &cobra.Command{
		Use:     "expire UUID [UUID...]",
		Aliases: []string{"rm", "remove"},
		Short:   "Expire silences",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return g.single(func(ss *session) *report.Section {
				return checks.ActionExpireSilence(ss.ctx, ss.env, g.o, args, expDry)
			})
		},
	}
	expire.Flags().BoolVar(&expDry, "dry-run", false, "Show what would be done")
	cmd.AddCommand(list, add, expire)
	return cmd
}

// ------------------------------------------------------------- defrag

func newDefragCommand(g *globals) *cobra.Command {
	do := checks.DefragOptions{}
	var act actionFlags
	cmd := &cobra.Command{
		Use:   "defrag",
		Short: "Defragment etcd member by member, leader last",
		Long: `Defragment etcd one member at a time: followers first, leader last. The
operation is refused when a member is unhealthy, and cluster health is checked
again after each member.`,
		Example: g.example("$CMD defrag --dry-run", "$CMD defrag --min-frag 30 --yes"),
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			ss, err := g.connect()
			if err != nil {
				return err
			}
			defer ss.cancel()
			ss.header("oc-upkit — etcd defragmentation")
			ss.add(checks.CheckEtcd(ss.ctx, ss.env, g.o))
			if !act.dryRun && !act.yes && !g.confirm(fmt.Sprintf("Cluster %s: defragment etcd?", ss.kc.Context)) {
				return nil
			}
			do.DryRun, do.Log = act.dryRun, g.streams.ErrOut
			ss.add(checks.ActionDefrag(ss.ctx, ss.env, g.o, do))
			return ss.finish("crit")
		},
	}
	f := cmd.Flags()
	f.Float64Var(&do.MinFragmentation, "min-frag", 0, "Only defragment members above this fragmentation (%)")
	f.DurationVar(&do.Pause, "pause", 15*time.Second, "Pause between two members")
	f.DurationVar(&do.CommandTimeout, "command-timeout", 60*time.Second, "Timeout of etcdctl defrag")
	f.BoolVar(&do.DisarmAlarms, "disarm-alarms", false, "Disarm etcd alarms (NOSPACE) after defragmentation")
	f.StringVar(&g.o.EtcdNamespace, "etcd-namespace", g.o.EtcdNamespace, "Namespace of the etcd pods")
	act.add(cmd)
	return cmd
}
