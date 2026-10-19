// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"github.com/itlinuxfr/oc-precheck/internal/report"
)

// DrainOptions configures the drain of a set of nodes selected by label,
// typically a whole room / availability zone.
type DrainOptions struct {
	Selector            string
	Parallel            int           // nodes drained at the same time
	PodParallel         int           // concurrent evictions per node
	Timeout             time.Duration // per node
	GracePeriod         int64         // -1 = the pod's terminationGracePeriodSeconds
	DeleteEmptyDir      bool
	Force               bool // delete pods without a controller
	IncludeControlPlane bool
	CordonOnly          bool
	DryRun              bool
	RetryInterval       time.Duration
	Log                 io.Writer
}

type podAction int

const (
	actEvict podAction = iota
	actDelete
	actSkip
	actBlock
)

type plannedPod struct {
	pod    corev1.Pod
	action podAction
	reason string
}

// DrainPlan is the result of the pre-drain analysis.
type DrainPlan struct {
	Nodes    []corev1.Node
	Pods     map[string][]plannedPod
	Blocking int
	Section  *report.Section
}

func classifyPod(p corev1.Pod, do DrainOptions) plannedPod {
	if _, mirror := p.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
		return plannedPod{p, actSkip, "static pod"}
	}
	ctrl := metav1.GetControllerOf(&p)
	if ctrl != nil && ctrl.Kind == "DaemonSet" {
		return plannedPod{p, actSkip, "DaemonSet"}
	}
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return plannedPod{p, actDelete, "completed"}
	}
	if ctrl == nil {
		if do.Force {
			return plannedPod{p, actDelete, "no controller (deleted, --force)"}
		}
		return plannedPod{p, actBlock, "pod without controller: it will not be recreated (--force to delete it)"}
	}
	for _, v := range p.Spec.Volumes {
		if v.EmptyDir != nil && !do.DeleteEmptyDir {
			return plannedPod{p, actBlock, "emptyDir volume " + v.Name + ": data will be lost (--delete-emptydir-data)"}
		}
	}
	return plannedPod{p, actEvict, ""}
}

func podsOnNode(ctx context.Context, env *Env, node string) ([]corev1.Pod, error) {
	l, err := env.CS.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
	if err != nil {
		return nil, err
	}
	return l.Items, nil
}

type capacity struct {
	nodes            int
	podSlots         int64
	cpuFree, memFree resource.Quantity
}

// PlanDrain analyses what draining the selected nodes implies: pods to
// evict, blockers, PDBs and remaining capacity per zone.
func PlanDrain(ctx context.Context, env *Env, o *Options, do DrainOptions) *DrainPlan {
	s := report.NewSection("drain-plan", "Drain: pre-flight analysis")
	plan := &DrainPlan{Pods: map[string][]plannedPod{}, Section: s}
	sel, err := labels.Parse(do.Selector)
	if err != nil || do.Selector == "" {
		s.Fail(fmt.Errorf("invalid selector %q", do.Selector), "e.g. --selector topology.kubernetes.io/zone=room-a")
		return plan
	}
	all, err := env.CS.CoreV1().Nodes().List(ctx, metaList)
	if err != nil {
		s.Fail(err, "")
		return plan
	}
	var targets, others []corev1.Node
	for _, n := range all.Items {
		if sel.Matches(labels.Set(n.Labels)) {
			targets = append(targets, n)
		} else {
			others = append(others, n)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	if len(targets) == 0 {
		s.Fail(fmt.Errorf("no node matches %q", do.Selector), "")
		return plan
	}
	var cp []string
	for _, n := range targets {
		if isControlPlane(n) {
			cp = append(cp, n.Name)
		}
	}
	if len(cp) > 0 && !do.IncludeControlPlane {
		s.Fail(fmt.Errorf("the selector includes control plane nodes: %s", strings.Join(cp, ", ")),
			"refused by default (--include-control-plane to override; never drain more than one control plane node at a time)")
		return plan
	}
	plan.Nodes = targets

	allPods, err := listActivePods(ctx, env)
	if err != nil {
		s.Fail(err, "")
		return plan
	}
	podsBy := map[string][]corev1.Pod{}
	for _, p := range allPods {
		podsBy[p.Spec.NodeName] = append(podsBy[p.Spec.NodeName], p)
	}

	tNodes := s.NewTable("Target nodes", "Node", "Roles", "Zone", "Status", "To evict", "To delete", "Skipped (DS/static)", "Blocking")
	tBlock := s.NewTable("Blocking pods", "Namespace", "Pod", "Node", "Reason")
	var evictCPU, evictMem resource.Quantity
	var toEvict []corev1.Pod
	for _, n := range targets {
		var ev, del, skip, block int
		for _, p := range podsBy[n.Name] {
			pp := classifyPod(p, do)
			plan.Pods[n.Name] = append(plan.Pods[n.Name], pp)
			switch pp.action {
			case actEvict:
				ev++
				toEvict = append(toEvict, p)
				c, m := podRequests(p)
				evictCPU.Add(c)
				evictMem.Add(m)
			case actDelete:
				del++
			case actSkip:
				skip++
			case actBlock:
				block++
				tBlock.AddRow(report.Crit, p.Namespace, p.Name, n.Name, pp.reason)
			}
		}
		plan.Blocking += block
		ready, _ := nodeReady(n)
		st := "Ready"
		if !ready {
			st = "NotReady"
		}
		if n.Spec.Unschedulable {
			st += ",SchedulingDisabled"
		}
		lvl := report.Pass
		if block > 0 {
			lvl = report.Crit
		}
		tNodes.AddRow(lvl, n.Name, nodeRoles(n), orDash(n.Labels[o.ZoneLabel]), st,
			fmt.Sprint(ev), fmt.Sprint(del), fmt.Sprint(skip), fmt.Sprint(block))
	}
	tBlock.Limit(o.MaxRows)
	if plan.Blocking > 0 {
		s.Add(report.Crit, "%d blocking pod(s): the drain is refused until they are handled (or use --force / --delete-emptydir-data)", plan.Blocking)
	}

	if pdbs, err := env.CS.PolicyV1().PodDisruptionBudgets("").List(ctx, metaList); err == nil {
		tPDB := s.NewTable("Affected PDBs", "Namespace", "PDB", "Pods on target nodes", "Allowed disruptions", "Effect")
		for _, pdb := range pdbs.Items {
			psel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err != nil || psel.Empty() {
				continue
			}
			n := 0
			for _, p := range toEvict {
				if p.Namespace == pdb.Namespace && psel.Matches(labels.Set(p.Labels)) {
					n++
				}
			}
			if n == 0 {
				continue
			}
			allowed := pdb.Status.DisruptionsAllowed
			switch {
			case allowed == 0:
				tPDB.AddRow(report.Warn, pdb.Namespace, pdb.Name, fmt.Sprint(n), "0", "BLOCKING until replacement pods are ready elsewhere")
				s.Escalate(report.Warn)
			case int32(n) > allowed:
				tPDB.AddRow(report.Info, pdb.Namespace, pdb.Name, fmt.Sprint(n), fmt.Sprint(allowed), "sequential evictions (slows the drain down)")
			case o.Verbose:
				tPDB.AddRow(report.Pass, pdb.Namespace, pdb.Name, fmt.Sprint(n), fmt.Sprint(allowed), "OK")
			}
		}
		tPDB.Limit(o.MaxRows)
	}

	// Remaining capacity per zone, on Ready schedulable non-control-plane nodes.
	byZone := map[string]*capacity{}
	total := &capacity{}
	workersTotal := 0
	for _, n := range all.Items {
		if !isControlPlane(n) {
			workersTotal++
		}
	}
	for _, n := range others {
		ready, _ := nodeReady(n)
		if !ready || n.Spec.Unschedulable || isControlPlane(n) {
			continue
		}
		zone := orDash(n.Labels[o.ZoneLabel])
		if byZone[zone] == nil {
			byZone[zone] = &capacity{}
		}
		var cpu, mem resource.Quantity
		for _, p := range podsBy[n.Name] {
			c, m := podRequests(p)
			cpu.Add(c)
			mem.Add(m)
		}
		freeCPU := n.Status.Allocatable.Cpu().DeepCopy()
		freeCPU.Sub(cpu)
		freeMem := n.Status.Allocatable.Memory().DeepCopy()
		freeMem.Sub(mem)
		slots := int64(0)
		if ap := n.Status.Allocatable.Pods(); ap != nil {
			slots = ap.Value() - int64(len(podsBy[n.Name]))
		}
		for _, c := range []*capacity{byZone[zone], total} {
			c.nodes++
			c.podSlots += slots
			c.cpuFree.Add(freeCPU)
			c.memFree.Add(freeMem)
		}
	}
	tCap := s.NewTable("Remaining capacity (Ready, schedulable, non control plane nodes)", "Zone", "Nodes", "Pod slots", "Free CPU (requests)", "Free memory (requests)")
	for _, z := range sortedKeys(byZone) {
		c := byZone[z]
		tCap.AddRow(report.Pass, z, fmt.Sprint(c.nodes), fmt.Sprint(c.podSlots), c.cpuFree.String(), humanBytes(c.memFree.Value()))
	}
	tCap.AddRow(report.Info, "TOTAL", fmt.Sprint(total.nodes), fmt.Sprint(total.podSlots), total.cpuFree.String(), humanBytes(total.memFree.Value()))
	tCap.AddRow(report.Info, "TO MOVE", "-", fmt.Sprint(len(toEvict)), evictCPU.String(), humanBytes(evictMem.Value()))

	if total.nodes == 0 {
		s.Add(report.Crit, "No worker node left to host the evicted pods")
	}
	if int64(len(toEvict)) > total.podSlots {
		s.Add(report.Crit, "Not enough pod slots: %d to move, %d available", len(toEvict), total.podSlots)
	}
	if evictCPU.Cmp(total.cpuFree) > 0 {
		s.Add(report.Crit, "CPU requests to move (%s) exceed the remaining free CPU (%s)", evictCPU.String(), total.cpuFree.String())
	}
	if evictMem.Cmp(total.memFree) > 0 {
		s.Add(report.Crit, "Memory requests to move (%s) exceed the remaining free memory (%s)", humanBytes(evictMem.Value()), humanBytes(total.memFree.Value()))
	}
	nonCP := 0
	for _, n := range targets {
		if !isControlPlane(n) {
			nonCP++
		}
	}
	if workersTotal > 0 && nonCP*2 >= workersTotal {
		s.Add(report.Warn, "%d/%d non control plane nodes targeted (≥ 50%% of the capacity)", nonCP, workersTotal)
	}
	s.Add(report.Info, "Affinity, topology spread constraints and nodeSelectors are not simulated: review workloads pinned to the zone")
	s.Summary = fmt.Sprintf("Selector %q: %d node(s), %d pod(s) to evict, %d blocking", do.Selector, len(targets), len(toEvict), plan.Blocking)
	return plan
}

func patchUnschedulable(ctx context.Context, env *Env, node string, v bool) error {
	body := fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, v)
	_, err := env.CS.CoreV1().Nodes().Patch(ctx, node, types.StrategicMergePatchType, []byte(body), metav1.PatchOptions{})
	return err
}

type drainResult struct {
	node             string
	evicted, deleted int
	failed           []string
	dur              time.Duration
	err              error
}

// ExecuteDrain cordons every selected node first, so that evicted pods do
// not land on another node of the same room, then drains them.
func ExecuteDrain(ctx context.Context, env *Env, o *Options, plan *DrainPlan, do DrainOptions) *report.Section {
	s := report.NewSection("drain", "Action: drain")
	logf := func(format string, a ...any) {
		if do.Log != nil {
			fmt.Fprintf(do.Log, "  [%s] "+format+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
		}
	}
	if do.Parallel < 1 {
		do.Parallel = 1
	}
	if do.PodParallel < 1 {
		do.PodParallel = 5
	}
	if do.RetryInterval <= 0 {
		do.RetryInterval = 5 * time.Second
	}
	if do.DryRun {
		s.Summary = fmt.Sprintf("dry run: %d node(s) would be cordoned then drained (%d at a time)", len(plan.Nodes), do.Parallel)
		s.Add(report.Info, "no change made")
		return s
	}

	for _, n := range plan.Nodes {
		if err := patchUnschedulable(ctx, env, n.Name, true); err != nil {
			return s.Fail(fmt.Errorf("cordon %s: %w", n.Name, err), "stopping")
		}
		logf("%s cordoned", n.Name)
	}
	if do.CordonOnly {
		s.Summary = fmt.Sprintf("%d node(s) cordoned (--cordon-only)", len(plan.Nodes))
		s.Add(report.Pass, "Cordon applied")
		return s
	}

	results := make([]drainResult, len(plan.Nodes))
	sem := make(chan struct{}, do.Parallel)
	var wg sync.WaitGroup
	for i, n := range plan.Nodes {
		wg.Add(1)
		go func(i int, node string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = drainNode(ctx, env, node, do, logf)
		}(i, n.Name)
	}
	wg.Wait()

	t := s.NewTable("", "Node", "Evicted", "Deleted", "Failures", "Duration", "Result")
	var ok int
	for _, r := range results {
		lvl, res := report.Pass, "drained"
		if r.err != nil || len(r.failed) > 0 {
			lvl, res = report.Crit, "INCOMPLETE"
			if r.err != nil {
				res += ": " + r.err.Error()
			}
			for _, f := range firstN(r.failed, 10) {
				s.Add(report.Crit, "%s: %s", r.node, f)
			}
		} else {
			ok++
		}
		t.AddRow(lvl, r.node, fmt.Sprint(r.evicted), fmt.Sprint(r.deleted), fmt.Sprint(len(r.failed)), humanDuration(r.dur), res)
		s.Escalate(lvl)
	}
	s.Summary = fmt.Sprintf("%d/%d node(s) drained — selector %q", ok, len(results), do.Selector)
	s.Add(report.Info, "Put the nodes back in service with: oc precheck uncordon --selector '%s'", do.Selector)
	return s
}

func drainNode(parent context.Context, env *Env, node string, do DrainOptions, logf func(string, ...any)) drainResult {
	start := time.Now()
	res := drainResult{node: node}
	ctx := parent
	if do.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, do.Timeout)
		defer cancel()
	}
	pods, err := podsOnNode(ctx, env, node)
	if err != nil {
		res.err = err
		return res
	}
	var work []plannedPod
	for _, p := range pods {
		pp := classifyPod(p, do)
		switch pp.action {
		case actBlock:
			res.failed = append(res.failed, fmt.Sprintf("%s/%s: %s", p.Namespace, p.Name, pp.reason))
		case actEvict, actDelete:
			work = append(work, pp)
		}
	}
	logf("%s: draining %d pod(s)", node, len(work))

	var mu sync.Mutex
	sem := make(chan struct{}, do.PodParallel)
	var wg sync.WaitGroup
	for _, pp := range work {
		wg.Add(1)
		go func(pp plannedPod) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			err := removePod(ctx, env, pp, do, logf, node)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				res.failed = append(res.failed, fmt.Sprintf("%s/%s: %v", pp.pod.Namespace, pp.pod.Name, err))
			case pp.action == actEvict:
				res.evicted++
			default:
				res.deleted++
			}
		}(pp)
	}
	wg.Wait()
	res.dur = time.Since(start)
	logf("%s: drained in %s (%d evicted, %d deleted, %d failure(s))", node, humanDuration(res.dur), res.evicted, res.deleted, len(res.failed))
	return res
}

// errEvictionBlocked wraps a timeout while a PDB keeps refusing evictions.
var errEvictionBlocked = errors.New("timed out while a PodDisruptionBudget blocks the eviction")

func removePod(ctx context.Context, env *Env, pp plannedPod, do DrainOptions, logf func(string, ...any), node string) error {
	p := pp.pod
	delOpts := metav1.DeleteOptions{}
	if do.GracePeriod >= 0 {
		g := do.GracePeriod
		delOpts.GracePeriodSeconds = &g
	}
	if pp.action == actDelete {
		if err := env.CS.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, delOpts); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else {
		warned := false
		for {
			err := env.CS.PolicyV1().Evictions(p.Namespace).Evict(ctx, &policyv1.Eviction{
				ObjectMeta:    metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace},
				DeleteOptions: &delOpts,
			})
			if err == nil || apierrors.IsNotFound(err) {
				break
			}
			if !apierrors.IsTooManyRequests(err) {
				return err
			}
			if !warned {
				logf("%s: %s/%s blocked by a PDB, retrying every %s", node, p.Namespace, p.Name, do.RetryInterval)
				warned = true
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: %v", errEvictionBlocked, ctx.Err())
			case <-time.After(do.RetryInterval):
			}
		}
	}
	// Wait until the pod is gone (or replaced by a new pod with the same name).
	for {
		cur, err := env.CS.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || (err == nil && cur.UID != p.UID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pod still present: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// Uncordon makes the selected nodes schedulable again.
func Uncordon(ctx context.Context, env *Env, selector string, dryRun bool) *report.Section {
	s := report.NewSection("uncordon", "Action: uncordon")
	nodes, err := env.CS.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return s.Fail(err, "")
	}
	if len(nodes.Items) == 0 {
		return s.Fail(fmt.Errorf("no node matches %q", selector), "")
	}
	t := s.NewTable("", "Node", "Before", "Result")
	for _, n := range nodes.Items {
		before := "schedulable"
		if n.Spec.Unschedulable {
			before = "SchedulingDisabled"
		}
		switch {
		case !n.Spec.Unschedulable:
			t.AddRow(report.Pass, n.Name, before, "already schedulable")
		case dryRun:
			t.AddRow(report.Info, n.Name, before, "dry run")
		default:
			if err := patchUnschedulable(ctx, env, n.Name, false); err != nil {
				t.AddRow(report.Crit, n.Name, before, err.Error())
				s.Escalate(report.Crit)
				continue
			}
			t.AddRow(report.Pass, n.Name, before, "uncordoned")
		}
	}
	s.Summary = fmt.Sprintf("%d node(s) — selector %q", len(nodes.Items), selector)
	return s
}
