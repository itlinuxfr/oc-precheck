// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/itlinuxfr/oc-precheck/internal/report"
)

// WatchOptions configures the live view.
type WatchOptions struct {
	Selector string
	Pods     bool // count pods per node (one extra list per refresh)
	HCP      bool // include HostedClusters/NodePools when on a hub
	Problems bool // only show nodes that are not Ready/schedulable/up to date
}

// shortConfig keeps the hash part of a rendered MachineConfig name.
func shortConfig(c string) string {
	if i := strings.LastIndex(c, "-"); i >= 0 && len(c)-i > 9 {
		return c[i+1 : i+9]
	}
	return c
}

// WatchSnapshot builds one refresh of the live view.
func WatchSnapshot(ctx context.Context, env *Env, o *Options, wo WatchOptions) []*report.Section {
	var out []*report.Section

	if cv, err := env.Dyn.Resource(gvrClusterVersion).Get(ctx, "version", metaGet); err == nil {
		out = append(out, watchClusterVersion(ctx, env, cv.Object))
	}

	var pools []poolSelector
	if !env.HostedControlPlane {
		if list, err := env.Dyn.Resource(gvrMCP).List(ctx, metaList); err == nil && len(list.Items) > 0 {
			s := report.NewSection("mcp", "MachineConfigPools")
			t := s.NewTable("", "Pool", "Paused", "Updated", "Ready", "Degraded", "Updating", "Target config")
			items := list.Items
			sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
			for _, mcp := range items {
				paused, _, _ := unstructured.NestedBool(mcp.Object, "spec", "paused")
				count := nestedInt(mcp.Object, "status", "machineCount")
				ready := nestedInt(mcp.Object, "status", "readyMachineCount")
				updated := nestedInt(mcp.Object, "status", "updatedMachineCount")
				deg := nestedInt(mcp.Object, "status", "degradedMachineCount")
				target, _, _ := unstructured.NestedString(mcp.Object, "spec", "configuration", "name")
				c := conditions(mcp.Object)
				lvl := report.Pass
				switch {
				case deg > 0 || isTrue(c, "Degraded"):
					lvl = report.Crit
				case updated < count || isTrue(c, "Updating"):
					lvl = report.Warn
				}
				s.Escalate(lvl)
				t.AddRow(lvl, mcp.GetName(), yesNo(paused), fmt.Sprintf("%d/%d", updated, count), fmt.Sprint(ready),
					fmt.Sprint(deg), orDash(c["Updating"].Status), shortConfig(target))
				if sel, ok := mcpSelector(mcp); ok {
					pools = append(pools, poolSelector{mcp.GetName(), sel})
				}
			}
			out = append(out, s)
		}
	}

	out = append(out, watchNodes(ctx, env, o, wo, pools))

	if wo.HCP {
		if hs := watchHostedClusters(ctx, env, o); hs != nil {
			out = append(out, hs)
		}
	}
	return out
}

func watchClusterVersion(ctx context.Context, env *Env, obj map[string]any) *report.Section {
	s := report.NewSection("clusterversion", "Cluster update")
	c := conditions(obj)
	desired, _, _ := unstructured.NestedString(obj, "status", "desired", "version")
	state := ""
	if hist, _, _ := unstructured.NestedSlice(obj, "status", "history"); len(hist) > 0 {
		if h, ok := hist[0].(map[string]any); ok {
			state = str(h["state"])
		}
	}
	s.Summary = fmt.Sprintf("Desired version %s — state %s", desired, orDash(state))
	if isTrue(c, "Progressing") {
		s.Add(report.Warn, "%s", oneLine(c["Progressing"].Message, 200))
	}
	if isTrue(c, "Failing") {
		s.Add(report.Crit, "Failing: %s", oneLine(c["Failing"].Message, 200))
	}
	cos, err := env.Dyn.Resource(gvrClusterOperator).List(ctx, metaList)
	if err != nil {
		return s
	}
	atVersion := 0
	var moving, broken []string
	for _, co := range cos.Items {
		cc := conditions(co.Object)
		vs, _, _ := unstructured.NestedSlice(co.Object, "status", "versions")
		for _, v := range vs {
			if m, ok := v.(map[string]any); ok && str(m["name"]) == "operator" && str(m["version"]) == desired {
				atVersion++
			}
		}
		if isTrue(cc, "Progressing") {
			moving = append(moving, co.GetName())
		}
		if cc["Available"].Status != "True" || isTrue(cc, "Degraded") {
			broken = append(broken, co.GetName())
		}
	}
	s.Add(report.Info, "ClusterOperators at version %s: %d/%d", desired, atVersion, len(cos.Items))
	if len(moving) > 0 {
		s.Add(report.Info, "Progressing: %s", strings.Join(moving, ", "))
	}
	if len(broken) > 0 {
		s.Add(report.Crit, "Unavailable/degraded: %s", strings.Join(broken, ", "))
	}
	return s
}

func watchNodes(ctx context.Context, env *Env, o *Options, wo WatchOptions, pools []poolSelector) *report.Section {
	s := report.NewSection("nodes", "Nodes")
	nl, err := env.CS.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: wo.Selector})
	if err != nil {
		return s.Fail(err, "")
	}
	podCount := map[string]int{}
	if wo.Pods {
		if pods, err := listActivePods(ctx, env); err == nil {
			for _, p := range pods {
				podCount[p.Spec.NodeName]++
			}
		}
	}
	nodes := nl.Items
	sort.Slice(nodes, func(i, j int) bool {
		ri, rj := nodeRoles(nodes[i]), nodeRoles(nodes[j])
		if ri != rj {
			return ri < rj
		}
		return nodes[i].Name < nodes[j].Name
	})
	headers := []string{"Node", "Roles", "Zone", "Status", "Since", "Kubelet", "MCD", "Config"}
	if wo.Pods {
		headers = append(headers, "Pods")
	}
	t := s.NewTable("", headers...)
	var ready, cordoned, upToDate, withMCO int
	for _, n := range nodes {
		ok, _ := nodeReady(n)
		var since metav1.Time
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady {
				since = c.LastTransitionTime
			}
		}
		status := "Ready"
		lvl := report.Pass
		if !ok {
			status, lvl = "NotReady", report.Crit
		} else {
			ready++
		}
		if n.Spec.Unschedulable {
			status += ",SchedDisabled"
			lvl = report.Worse(lvl, report.Warn)
			cordoned++
		}
		mcd := n.Annotations[annMCState]
		cur, want := n.Annotations[annMCCurrent], n.Annotations[annMCDesired]
		cfg := "-"
		if cur != "" {
			withMCO++
			if cur == want {
				cfg = "✓ " + shortConfig(cur)
				upToDate++
			} else {
				cfg = shortConfig(cur) + " → " + shortConfig(want)
				lvl = report.Worse(lvl, report.Warn)
			}
		}
		switch {
		case mcd == "Degraded":
			lvl = report.Crit
		case mcd != "" && mcd != "Done":
			lvl = report.Worse(lvl, report.Warn)
		}
		if wo.Problems && lvl == report.Pass {
			continue
		}
		row := []string{n.Name, nodeRoles(n), orDash(n.Labels[o.ZoneLabel]), status, age(since.Time),
			n.Status.NodeInfo.KubeletVersion, orDash(mcd), cfg}
		if wo.Pods {
			row = append(row, fmt.Sprint(podCount[n.Name]))
		}
		t.AddRow(lvl, row...)
		s.Escalate(lvl)
	}
	s.Summary = fmt.Sprintf("%d node(s) — %d Ready, %d cordoned", len(nodes), ready, cordoned)
	if withMCO > 0 {
		s.Summary += fmt.Sprintf(", config up to date %d/%d", upToDate, withMCO)
	}
	if len(pools) > 0 {
		var orphan []string
		for _, n := range nodes {
			if len(poolsOf(pools, n)) == 0 {
				orphan = append(orphan, n.Name)
			}
		}
		if len(orphan) > 0 {
			s.Add(report.Warn, "Outside any pool: %s", strings.Join(orphan, ", "))
		}
	}
	return s
}

func watchHostedClusters(ctx context.Context, env *Env, o *Options) *report.Section {
	hcs, err := CollectHostedClusters(ctx, env, o, false)
	if err != nil || len(hcs) == 0 {
		return nil
	}
	s := report.NewSection("hostedclusters", "HostedClusters and NodePools")
	th := s.NewTable("", "HostedCluster", "CP version", "Target", "Update", "Available", "Progressing", "NodePools ready", "Nodes")
	tn := s.NewTable("", "NodePool", "Version", "Replicas", "Ready", "Updating version", "Updating config")
	for _, h := range hcs {
		lvl := report.Pass
		switch {
		case h.Cond["Available"].Status != "True" || isTrue(h.Cond, "Degraded"):
			lvl = report.Crit
		case isTrue(h.Cond, "Progressing") || (h.Desired != "" && h.Desired != h.Version):
			lvl = report.Warn
		}
		cur, want := h.NodeCounts()
		target := "-"
		if h.Desired != h.Version {
			target = h.Desired
		}
		th.AddRow(lvl, h.Key(), orDash(h.Version), orDash(target), orDash(h.HistoryState), condCell(h.Cond, "Available"),
			condCell(h.Cond, "Progressing"), fmt.Sprintf("%d/%d", h.ReadyPools(), len(h.NodePools)), fmt.Sprintf("%d/%d", cur, want))
		s.Escalate(lvl)
		for _, np := range h.NodePools {
			tn.AddRow(npLevel(np), np.Namespace+"/"+np.Name, orDash(np.Version), np.Replicas(), condCell(np.Cond, "Ready"),
				condCell(np.Cond, "UpdatingVersion"), condCell(np.Cond, "UpdatingConfig"))
		}
	}
	s.Summary = fmt.Sprintf("%d HostedCluster(s)", len(hcs))
	return s
}
