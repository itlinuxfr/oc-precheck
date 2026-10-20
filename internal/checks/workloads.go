// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

const (
	annMCState   = "machineconfiguration.openshift.io/state"
	annMCCurrent = "machineconfiguration.openshift.io/currentConfig"
	annMCDesired = "machineconfiguration.openshift.io/desiredConfig"
	annMCReason  = "machineconfiguration.openshift.io/reason"
	rolePrefix   = "node-role.kubernetes.io/"
)

// ----------------------------------------------------------------- MCP

type poolSelector struct {
	name     string
	selector labels.Selector
}

func mcpSelector(mcp unstructured.Unstructured) (labels.Selector, bool) {
	selMap, found, _ := unstructured.NestedMap(mcp.Object, "spec", "nodeSelector")
	if !found {
		return nil, false
	}
	var ls metav1.LabelSelector
	if runtime.DefaultUnstructuredConverter.FromUnstructured(selMap, &ls) != nil {
		return nil, false
	}
	sel, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil || sel.Empty() {
		return nil, false
	}
	return sel, true
}

func poolsOf(pools []poolSelector, n corev1.Node) []string {
	var matched []string
	for _, p := range pools {
		if p.selector.Matches(labels.Set(n.Labels)) {
			matched = append(matched, p.name)
		}
	}
	return matched
}

// CheckMCP reports MachineConfigPool state, paused pools and nodes not
// covered by any pool.
func CheckMCP(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("mcp", "MachineConfigPools")
	if env.HostedControlPlane {
		return s.Skipped("hosted control plane: no MCP, nodes are managed by NodePools (use the hostedclusters check on the hub)")
	}
	list, err := env.Dyn.Resource(gvrMCP).List(ctx, metaList)
	if err != nil {
		if notInstalled(err) {
			return s.Skipped("MachineConfigPool API not served")
		}
		return s.Fail(err, "")
	}
	nodes, err := env.CS.CoreV1().Nodes().List(ctx, metaList)
	if err != nil {
		return s.Fail(err, "")
	}
	r := newRuler(s, o)

	var pools []poolSelector
	t := s.NewTable("", "Pool", "Paused", "Machines", "Ready", "Updated", "Degraded", "Updating", "State", "Rendered config")
	var paused, updating, degraded []string
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
	for _, mcp := range items {
		name := mcp.GetName()
		isPaused, _, _ := unstructured.NestedBool(mcp.Object, "spec", "paused")
		count := nestedInt(mcp.Object, "status", "machineCount")
		ready := nestedInt(mcp.Object, "status", "readyMachineCount")
		updated := nestedInt(mcp.Object, "status", "updatedMachineCount")
		deg := nestedInt(mcp.Object, "status", "degradedMachineCount")
		rendered, _, _ := unstructured.NestedString(mcp.Object, "status", "configuration", "name")
		c := conditions(mcp.Object)

		lvl, state := report.Pass, "up to date"
		switch {
		case isTrue(c, "Degraded") || deg > 0:
			state = "DEGRADED"
			msg := firstNonEmpty(c["NodeDegraded"].Message, c["RenderDegraded"].Message, c["Degraded"].Message)
			if l, kept := r.add("mcp.degraded", "", name, "Pool %s is degraded: %s", name, oneLine(msg, 250)); kept {
				lvl = report.Worse(lvl, l)
				degraded = append(degraded, name)
			}
		case isTrue(c, "Updating") || updated < count:
			state = "UPDATING"
			if l, kept := r.eval("mcp.updating", "", name); kept {
				lvl = report.Worse(lvl, l)
				updating = append(updating, name)
			}
		}
		if isPaused {
			if state == "up to date" {
				state = "paused"
				if updated < count {
					state = "paused (update pending)"
				}
			}
			if l, kept := r.eval("mcp.paused", "", name); kept {
				lvl = report.Worse(lvl, l)
				paused = append(paused, name)
			}
		}
		s.Escalate(lvl)
		t.AddRow(lvl, name, yesNo(isPaused), fmt.Sprint(count), fmt.Sprint(ready), fmt.Sprint(updated),
			fmt.Sprint(deg), c["Updating"].Status, state, rendered)
		if sel, ok := mcpSelector(mcp); ok {
			pools = append(pools, poolSelector{name, sel})
		}
	}
	if len(paused) > 0 {
		s.AddRule("mcp.paused", effectiveLevel(o, "mcp.paused"), fmt.Sprintf(
			"PAUSED pools: %s — their nodes will not receive the new configuration while paused", strings.Join(paused, ", ")))
	}
	if len(updating) > 0 {
		s.AddRule("mcp.updating", effectiveLevel(o, "mcp.updating"), "Pools updating: "+strings.Join(updating, ", "))
	}

	tn := s.NewTable("Nodes to review (MCO)", "Node", "Pools", "MCD state", "Current = desired", "Reason")
	var orphan []string
	for _, n := range nodes.Items {
		matched := poolsOf(pools, n)
		var custom []string
		for _, m := range matched {
			if m != "worker" && m != "master" {
				custom = append(custom, m)
			}
		}
		ann := n.Annotations
		state := ann[annMCState]
		sameCfg := ann[annMCCurrent] != "" && ann[annMCCurrent] == ann[annMCDesired]
		rule, reason := "", ""
		switch {
		case len(matched) == 0:
			rule, reason = "mcp.node-orphan", "node outside any MachineConfigPool: it will NOT be updated"
		case len(custom) > 1:
			rule, reason = "mcp.node-multi-pool", "node in several custom pools: "+strings.Join(custom, ", ")
		case state == "Degraded":
			rule, reason = "mcp.node-degraded", ann[annMCReason]
		case state != "" && state != "Done":
			rule, reason = "mcp.node-working", "machine-config-daemon state "+state
		case !sameCfg:
			rule, reason = "mcp.node-config-mismatch", "currentConfig differs from desiredConfig"
		}
		cells := []string{n.Name, orDash(strings.Join(matched, ",")), orDash(state), yesNo(sameCfg), oneLine(reason, 120)}
		if rule != "" {
			if _, kept := r.row(tn, rule, "", n.Name, cells...); kept {
				if rule == "mcp.node-orphan" {
					orphan = append(orphan, n.Name)
				}
				continue
			}
		}
		if o.Verbose {
			tn.AddRow(report.Pass, cells...)
		}
	}
	tn.Limit(o.MaxRows)
	if len(orphan) > 0 {
		s.AddRule("mcp.node-orphan", effectiveLevel(o, "mcp.node-orphan"), fmt.Sprintf("%d node(s) outside any pool: %s", len(orphan), strings.Join(orphan, ", ")))
	}
	s.Summary = fmt.Sprintf("%d pool(s) — %d paused, %d updating, %d degraded", len(items), len(paused), len(updating), len(degraded))
	r.done()
	return s
}

// ----------------------------------------------------------------- PDB

// CheckPDB lists PDBs that are violated or that will block node drains.
func CheckPDB(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("pdb", "PodDisruptionBudgets")
	list, err := env.CS.PolicyV1().PodDisruptionBudgets("").List(ctx, metaList)
	if err != nil {
		return s.Fail(err, "")
	}
	r := newRuler(s, o)
	t := s.NewTable("", "Namespace", "PDB", "Budget", "Expected", "Healthy", "Desired", "Allowed disruptions", "Problem")
	var violated, blocking, empty int
	items := list.Items
	sort.Slice(items, func(i, j int) bool {
		if items[i].Namespace != items[j].Namespace {
			return items[i].Namespace < items[j].Namespace
		}
		return items[i].Name < items[j].Name
	})
	for _, p := range items {
		st := p.Status
		budget := "-"
		switch {
		case p.Spec.MinAvailable != nil:
			budget = "minAvailable " + p.Spec.MinAvailable.String()
		case p.Spec.MaxUnavailable != nil:
			budget = "maxUnavailable " + p.Spec.MaxUnavailable.String()
		}
		rule, problem := "", ""
		switch {
		case st.ExpectedPods == 0:
			rule, problem = "pdb.empty", "selects no pod"
		case st.CurrentHealthy < st.DesiredHealthy:
			rule, problem = "pdb.violated", "VIOLATED (healthy < desired)"
		case st.DisruptionsAllowed == 0:
			rule, problem = "pdb.blocking", "0 allowed disruptions: will block drains"
		}
		cells := []string{p.Namespace, p.Name, budget, fmt.Sprint(st.ExpectedPods), fmt.Sprint(st.CurrentHealthy),
			fmt.Sprint(st.DesiredHealthy), fmt.Sprint(st.DisruptionsAllowed), problem}
		if rule != "" {
			if lvl, kept := r.eval(rule, p.Namespace, p.Name); kept {
				switch rule {
				case "pdb.empty":
					empty++
				case "pdb.violated":
					violated++
				case "pdb.blocking":
					blocking++
				}
				if lvl != report.Info || o.Verbose {
					t.AddRow(lvl, cells...)
				}
				s.Escalate(lvl)
				continue
			}
		}
		if o.Verbose {
			t.AddRow(report.Pass, cells...)
		}
	}
	t.Limit(o.MaxRows)
	s.Summary = fmt.Sprintf("%d PDB(s) — %d violated, %d blocking drains, %d selecting no pod", len(items), violated, blocking, empty)
	if violated+blocking > 0 {
		s.Add(report.Info, "The MCO retries evictions forever: a blocking PDB stalls the pool update (MCDDrainError alert)")
	}
	r.done()
	return s
}

// --------------------------------------------------------------- nodes

type nodeAgg struct {
	nodes int
	pods  int
}

// listPods lists pods with paging. activeOnly excludes Succeeded/Failed.
func listPods(ctx context.Context, env *Env, ns string, activeOnly bool, labelSelector string) ([]corev1.Pod, error) {
	var all []corev1.Pod
	opts := metav1.ListOptions{Limit: 500, LabelSelector: labelSelector}
	if activeOnly {
		opts.FieldSelector = "status.phase!=Succeeded,status.phase!=Failed"
	}
	for {
		l, err := env.CS.CoreV1().Pods(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, l.Items...)
		if l.Continue == "" {
			return all, nil
		}
		opts.Continue = l.Continue
	}
}

func listActivePods(ctx context.Context, env *Env) ([]corev1.Pod, error) {
	return listPods(ctx, env, "", true, "")
}

func nodeRoles(n corev1.Node) string {
	var r []string
	for k := range n.Labels {
		if strings.HasPrefix(k, rolePrefix) {
			r = append(r, strings.TrimPrefix(k, rolePrefix))
		}
	}
	sort.Strings(r)
	if len(r) == 0 {
		return "-"
	}
	return strings.Join(r, ",")
}

func isControlPlane(n corev1.Node) bool {
	_, m := n.Labels[rolePrefix+"master"]
	_, c := n.Labels[rolePrefix+"control-plane"]
	return m || c
}

func nodeReady(n corev1.Node) (bool, string) {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue, c.Reason
		}
	}
	return false, "unknown"
}

func podRequests(p corev1.Pod) (cpu, mem resource.Quantity) {
	for _, c := range p.Spec.Containers {
		if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			cpu.Add(q)
		}
		if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			mem.Add(q)
		}
	}
	return
}

func pct(used, total resource.Quantity) float64 {
	if total.IsZero() {
		return 0
	}
	return float64(used.MilliValue()) * 100 / float64(total.MilliValue())
}

// CheckNodes reports node state and the pod distribution by node, zone,
// role and custom labels.
func CheckNodes(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("nodes", "Nodes and pod distribution")
	sel := labels.Everything()
	if o.NodeSelector != "" {
		var err error
		if sel, err = labels.Parse(o.NodeSelector); err != nil {
			return s.Fail(err, "invalid --node-selector")
		}
	}
	nl, err := env.CS.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return s.Fail(err, "")
	}
	pods, err := listActivePods(ctx, env)
	if err != nil {
		return s.Fail(err, "")
	}
	r := newRuler(s, o)
	podsByNode := map[string][]corev1.Pod{}
	for _, p := range pods {
		if p.Spec.NodeName != "" {
			podsByNode[p.Spec.NodeName] = append(podsByNode[p.Spec.NodeName], p)
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

	headers := []string{"Node", "Roles", "Status", "Zone", "Pods", "Pods %", "CPU req %", "Mem req %", "Kubelet"}
	headers = append(headers, o.GroupByLabels...)
	t := s.NewTable("Per node", headers...)

	byZone := map[string]*nodeAgg{}
	byRole := map[string]*nodeAgg{}
	byLabel := map[string]map[string]*nodeAgg{}
	for _, k := range o.GroupByLabels {
		byLabel[k] = map[string]*nodeAgg{}
	}
	kubelets := map[string]bool{}
	var notReady, cordoned, pressure []string
	zoneLabelSeen := false
	totalPods := 0
	inc := func(m map[string]*nodeAgg, k string, pods int) {
		if m[k] == nil {
			m[k] = &nodeAgg{}
		}
		m[k].nodes++
		m[k].pods += pods
	}

	for _, n := range nodes {
		np := podsByNode[n.Name]
		count := len(np)
		totalPods += count
		var cpu, mem resource.Quantity
		for _, p := range np {
			c, m := podRequests(p)
			cpu.Add(c)
			mem.Add(m)
		}
		allocPods := n.Status.Allocatable.Pods()
		podPct := 0.0
		if allocPods != nil && allocPods.Value() > 0 {
			podPct = float64(count) * 100 / float64(allocPods.Value())
		}
		cpuPct := pct(cpu, *n.Status.Allocatable.Cpu())
		memPct := pct(mem, *n.Status.Allocatable.Memory())

		lvl := report.Pass
		bump := func(l report.Level, kept bool) bool {
			if kept {
				lvl = report.Worse(lvl, l)
			}
			return kept
		}
		ready, reason := nodeReady(n)
		status := "Ready"
		if !ready {
			status = "NotReady"
			if reason != "" {
				status += " (" + reason + ")"
			}
			if bump(r.eval("nodes.not-ready", "", n.Name)) {
				notReady = append(notReady, n.Name)
			}
		}
		if n.Spec.Unschedulable {
			status += ",SchedulingDisabled"
			if bump(r.eval("nodes.cordoned", "", n.Name)) {
				cordoned = append(cordoned, n.Name)
			}
		}
		for _, c := range n.Status.Conditions {
			if (c.Type == corev1.NodeMemoryPressure || c.Type == corev1.NodeDiskPressure || c.Type == corev1.NodePIDPressure) &&
				c.Status == corev1.ConditionTrue {
				status += "," + string(c.Type)
				if bump(r.eval("nodes.pressure", "", n.Name)) {
					pressure = append(pressure, fmt.Sprintf("%s(%s)", n.Name, c.Type))
				}
			}
		}
		if o.NodePodCapacityPct > 0 && podPct >= o.NodePodCapacityPct {
			bump(r.add("nodes.pod-capacity", "", n.Name, "Node %s uses %.0f%% of its pod capacity", n.Name, podPct))
		}

		zone := n.Labels[o.ZoneLabel]
		if zone != "" {
			zoneLabelSeen = true
		} else {
			zone = "<none>"
		}
		roles := nodeRoles(n)
		kubelets[n.Status.NodeInfo.KubeletVersion] = true
		allocStr := "?"
		if allocPods != nil {
			allocStr = fmt.Sprint(allocPods.Value())
		}
		row := []string{n.Name, roles, status, zone,
			fmt.Sprintf("%d/%s", count, allocStr), fmt.Sprintf("%.0f%%", podPct),
			fmt.Sprintf("%.0f%%", cpuPct), fmt.Sprintf("%.0f%%", memPct), n.Status.NodeInfo.KubeletVersion}
		for _, k := range o.GroupByLabels {
			v := n.Labels[k]
			row = append(row, orDash(v))
			if v == "" {
				v = "<unset>"
			}
			inc(byLabel[k], v, count)
		}
		t.AddRow(lvl, row...)
		s.Escalate(lvl)
		inc(byZone, zone, count)
		inc(byRole, roles, count)
	}

	aggTable := func(title, keyHeader string, m map[string]*nodeAgg) {
		at := s.NewTable(title, keyHeader, "Nodes", "Pods", "Pods / node")
		for _, k := range sortedKeys(m) {
			a := m[k]
			avg := 0.0
			if a.nodes > 0 {
				avg = float64(a.pods) / float64(a.nodes)
			}
			at.AddRow(report.Pass, k, fmt.Sprint(a.nodes), fmt.Sprint(a.pods), fmt.Sprintf("%.1f", avg))
		}
	}
	aggTable("Per zone ("+o.ZoneLabel+")", "Zone", byZone)
	aggTable("Per role", "Roles", byRole)
	for _, k := range o.GroupByLabels {
		aggTable("Per label "+k, "Value", byLabel[k])
	}

	if len(notReady) > 0 {
		s.AddRule("nodes.not-ready", effectiveLevel(o, "nodes.not-ready"), "NotReady node(s): "+strings.Join(notReady, ", "))
	}
	if len(cordoned) > 0 {
		s.AddRule("nodes.cordoned", effectiveLevel(o, "nodes.cordoned"), "Cordoned (SchedulingDisabled) node(s): "+strings.Join(cordoned, ", "))
	}
	if len(pressure) > 0 {
		s.AddRule("nodes.pressure", effectiveLevel(o, "nodes.pressure"), "Resource pressure: "+strings.Join(pressure, ", "))
	}
	if !zoneLabelSeen {
		s.Add(report.Info, "No node carries the %s label (see --zone-label)", o.ZoneLabel)
	}
	if len(kubelets) > 1 {
		s.Add(report.Info, "Mixed kubelet versions: %s", strings.Join(sortedKeys(kubelets), ", "))
	}
	s.Summary = fmt.Sprintf("%d node(s), %d active pod(s) — %d NotReady, %d cordoned", len(nodes), totalPods, len(notReady), len(cordoned))
	r.done()
	return s
}

// ----------------------------------------------------------------- CSR

// CheckCSR lists pending certificate signing requests.
func CheckCSR(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("csr", "Pending CertificateSigningRequests")
	list, err := env.CS.CertificatesV1().CertificateSigningRequests().List(ctx, metaList)
	if err != nil {
		return s.Fail(err, "")
	}
	r := newRuler(s, o)
	t := s.NewTable("", "CSR", "Requestor", "Signer", "Age")
	pending := 0
	for _, c := range list.Items {
		if len(c.Status.Conditions) > 0 {
			continue
		}
		if _, kept := r.row(t, "csr.pending", "", c.Name, c.Name, c.Spec.Username, c.Spec.SignerName, age(c.CreationTimestamp.Time)); kept {
			pending++
		}
	}
	t.Limit(o.MaxRows)
	if pending > 0 {
		s.Summary = fmt.Sprintf("%d CSR(s) waiting for approval (out of %d)", pending, len(list.Items))
		s.Add(report.Info, "Check their legitimacy before approving (oc adm certificate approve)")
	} else {
		s.Summary = fmt.Sprintf("No pending CSR (%d in total)", len(list.Items))
	}
	r.done()
	return s
}
