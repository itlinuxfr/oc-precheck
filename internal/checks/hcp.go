// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// NodePoolInfo is the consolidated view of a HyperShift NodePool.
type NodePoolInfo struct {
	Namespace, Name, Cluster string
	Version                  string
	Desired, Current         int64
	Autoscaling              bool
	Min, Max                 int64
	UpgradeType              string
	AutoRepair               bool
	Cond                     map[string]condition
}

// Replicas renders the replica column.
func (np *NodePoolInfo) Replicas() string {
	if np.Autoscaling {
		return fmt.Sprintf("%d (auto %d-%d)", np.Current, np.Min, np.Max)
	}
	return fmt.Sprintf("%d/%d", np.Current, np.Desired)
}

// Updating reports whether a version or configuration rollout is running.
func (np *NodePoolInfo) Updating() bool {
	return isTrue(np.Cond, "UpdatingVersion") || isTrue(np.Cond, "UpdatingConfig")
}

// HostedClusterInfo is the consolidated view of a HostedCluster as seen
// from the hub (management cluster).
type HostedClusterInfo struct {
	Namespace, Name      string
	Version              string // last completed control plane version
	Desired              string
	HistoryState         string
	PausedUntil          string
	Cond                 map[string]condition
	CPNamespace          string
	CPPods, CPNotReady   int
	CPNotReadyNames      []string
	EtcdReady, EtcdTotal int
	ACM                  string // "" = no ManagedCluster, otherwise its Available status
	NodePools            []*NodePoolInfo
}

// Key returns namespace/name.
func (h *HostedClusterInfo) Key() string { return h.Namespace + "/" + h.Name }

// NodeCounts returns the current and desired node counts over all pools.
func (h *HostedClusterInfo) NodeCounts() (cur, want int64) {
	for _, np := range h.NodePools {
		cur += np.Current
		if np.Autoscaling {
			want += np.Current
		} else {
			want += np.Desired
		}
	}
	return
}

// ReadyPools counts NodePools with Ready=True.
func (h *HostedClusterInfo) ReadyPools() int {
	n := 0
	for _, np := range h.NodePools {
		if isTrue(np.Cond, "Ready") {
			n++
		}
	}
	return n
}

// ErrNotHub is returned when the HyperShift API is not served.
var ErrNotHub = errors.New("hypershift.openshift.io API not served: this cluster is not a HyperShift / MCE hub")

// CollectHostedClusters gathers HostedClusters and NodePools and, when
// withDetails is set, hosted control plane pods and ACM registration.
func CollectHostedClusters(ctx context.Context, env *Env, o *Options, withDetails bool) ([]*HostedClusterInfo, error) {
	hcs, err := env.Dyn.Resource(gvrHostedCluster).Namespace("").List(ctx, metaList)
	if err != nil {
		if notInstalled(err) {
			return nil, ErrNotHub
		}
		return nil, err
	}
	byKey := map[string]*HostedClusterInfo{}
	var out []*HostedClusterInfo
	for _, hc := range hcs.Items {
		if !o.HCPSelected(hc.GetNamespace(), hc.GetName()) {
			continue
		}
		h := &HostedClusterInfo{
			Namespace:   hc.GetNamespace(),
			Name:        hc.GetName(),
			Cond:        conditions(hc.Object),
			CPNamespace: hc.GetNamespace() + "-" + hc.GetName(),
		}
		h.Desired, _, _ = unstructured.NestedString(hc.Object, "status", "version", "desired", "version")
		h.PausedUntil, _, _ = unstructured.NestedString(hc.Object, "spec", "pausedUntil")
		hist, _, _ := unstructured.NestedSlice(hc.Object, "status", "version", "history")
		for i, e := range hist {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if i == 0 {
				h.HistoryState = str(m["state"])
			}
			if h.Version == "" && str(m["state"]) == "Completed" {
				h.Version = str(m["version"])
			}
		}
		if h.Version == "" {
			h.Version = h.Desired
		}
		byKey[h.Key()] = h
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })

	if nps, err := env.Dyn.Resource(gvrNodePool).Namespace("").List(ctx, metaList); err == nil {
		for _, np := range nps.Items {
			cluster, _, _ := unstructured.NestedString(np.Object, "spec", "clusterName")
			h := byKey[np.GetNamespace()+"/"+cluster]
			if h == nil {
				continue
			}
			info := &NodePoolInfo{Namespace: np.GetNamespace(), Name: np.GetName(), Cluster: cluster, Cond: conditions(np.Object)}
			info.Version, _, _ = unstructured.NestedString(np.Object, "status", "version")
			info.Desired = nestedInt(np.Object, "spec", "replicas")
			info.Current = nestedInt(np.Object, "status", "replicas")
			if _, found, _ := unstructured.NestedMap(np.Object, "spec", "autoScaling"); found {
				info.Autoscaling = true
				info.Min = nestedInt(np.Object, "spec", "autoScaling", "min")
				info.Max = nestedInt(np.Object, "spec", "autoScaling", "max")
			}
			info.UpgradeType, _, _ = unstructured.NestedString(np.Object, "spec", "management", "upgradeType")
			info.AutoRepair, _, _ = unstructured.NestedBool(np.Object, "spec", "management", "autoRepair")
			h.NodePools = append(h.NodePools, info)
		}
	}
	for _, h := range out {
		sort.Slice(h.NodePools, func(i, j int) bool { return h.NodePools[i].Name < h.NodePools[j].Name })
	}

	if withDetails {
		for _, h := range out {
			pods, err := env.CS.CoreV1().Pods(h.CPNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				continue
			}
			for _, p := range pods.Items {
				if p.Status.Phase == corev1.PodSucceeded {
					continue
				}
				h.CPPods++
				st := GetPodStatus(p)
				ready := p.Status.Phase == corev1.PodRunning && st.Ready == st.Total
				if !ready {
					h.CPNotReady++
					h.CPNotReadyNames = append(h.CPNotReadyNames, fmt.Sprintf("%s (%s %d/%d)", p.Name, st.Status, st.Ready, st.Total))
				}
				if p.Labels["app"] == "etcd" {
					h.EtcdTotal++
					if ready {
						h.EtcdReady++
					}
				}
			}
			if mc, err := env.Dyn.Resource(gvrManagedCluster).Get(ctx, h.Name, metaGet); err == nil {
				h.ACM = orDash(conditions(mc.Object)["ManagedClusterConditionAvailable"].Status)
			}
		}
	}
	return out, nil
}

func condCell(c map[string]condition, t string) string {
	if v, ok := c[t]; ok {
		return v.Status
	}
	return "-"
}

// npLevel is the display level of a NodePool row.
func npLevel(np *NodePoolInfo) report.Level {
	switch {
	case isFalse(np.Cond, "Ready") || isFalse(np.Cond, "AllNodesHealthy"):
		return report.Crit
	case np.Updating() || (!np.Autoscaling && np.Current != np.Desired):
		return report.Warn
	}
	return report.Pass
}

// CheckHostedClusters establishes a global status per HostedCluster from
// the hub: control plane conditions, hosted etcd and API server, control
// plane pods, NodePools and ACM registration.
func CheckHostedClusters(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("hostedclusters", "HostedClusters and NodePools (hub)")
	hcs, err := CollectHostedClusters(ctx, env, o, true)
	if errors.Is(err, ErrNotHub) {
		return s.Skipped(err.Error())
	}
	if err != nil {
		return s.Fail(err, "")
	}
	if len(hcs) == 0 {
		return s.Skipped("no HostedCluster (or none matches --hcp-filter)")
	}
	r := newRuler(s, o)

	tHC := s.NewTable("HostedCluster global status", "HostedCluster", "CP version", "Target", "Update", "Available",
		"Degraded", "Progressing", "etcd", "API", "CP pods", "NodePools", "Nodes", "ACM", "Status")
	tNP := s.NewTable("NodePools", "NodePool", "HostedCluster", "Version", "Replicas", "Ready", "Nodes healthy",
		"Updating version", "Updating config", "Upgrade type", "Auto-repair")
	counts := map[report.Level]int{}

	for _, h := range hcs {
		global := report.Pass
		check := func(rule string, cond bool, format string, args ...any) {
			if !cond {
				return
			}
			if l, kept := r.add(rule, h.Namespace, h.Name, "["+h.Key()+"] "+format, args...); kept {
				global = report.Worse(global, l)
			}
		}
		c := h.Cond
		check("hcp.unavailable", c["Available"].Status != "True", "Available=%s: %s", orDash(c["Available"].Status), oneLine(c["Available"].Message, 200))
		check("hcp.degraded", isTrue(c, "Degraded"), "Degraded: %s", oneLine(c["Degraded"].Message, 200))
		check("hcp.etcd-unavailable", isFalse(c, "EtcdAvailable"), "EtcdAvailable=False: %s", oneLine(c["EtcdAvailable"].Message, 200))
		check("hcp.kas-unavailable", isFalse(c, "KubeAPIServerAvailable"), "KubeAPIServerAvailable=False: %s", oneLine(c["KubeAPIServerAvailable"].Message, 200))
		check("hcp.cv-failing", isFalse(c, "ClusterVersionSucceeding"), "ClusterVersionSucceeding=False: %s", oneLine(c["ClusterVersionSucceeding"].Message, 200))
		check("hcp.invalid-release", isFalse(c, "ValidReleaseImage"), "ValidReleaseImage=False: %s", oneLine(c["ValidReleaseImage"].Message, 200))
		check("hcp.progressing", isTrue(c, "Progressing") || h.HistoryState == "Partial" || (h.Desired != "" && h.Desired != h.Version),
			"control plane update in progress (%s → %s, state %s)", h.Version, h.Desired, orDash(h.HistoryState))
		check("hcp.not-upgradeable", isFalse(c, "ClusterVersionUpgradeable"), "ClusterVersionUpgradeable=False: %s", oneLine(c["ClusterVersionUpgradeable"].Message, 200))
		check("hcp.reconciliation-paused", h.PausedUntil != "", "reconciliation paused (spec.pausedUntil=%s)", h.PausedUntil)
		check("hcp.cp-pods", h.CPNotReady > 0, "%d pod(s) not ready in %s: %s", h.CPNotReady, h.CPNamespace, strings.Join(firstN(h.CPNotReadyNames, 5), ", "))
		check("hcp.acm-unavailable", h.ACM != "" && h.ACM != "True", "ACM ManagedCluster Available=%s", h.ACM)

		for _, np := range h.NodePools {
			npCheck := func(rule string, cond bool, format string, args ...any) {
				if !cond {
					return
				}
				if l, kept := r.add(rule, np.Namespace, np.Name, "[NodePool "+np.Namespace+"/"+np.Name+"] "+format, args...); kept {
					global = report.Worse(global, l)
				}
			}
			npCheck("hcp.np-not-ready", isFalse(np.Cond, "Ready"), "Ready=False: %s", oneLine(np.Cond["Ready"].Message, 200))
			npCheck("hcp.np-unhealthy-nodes", isFalse(np.Cond, "AllNodesHealthy"), "AllNodesHealthy=False: %s", oneLine(np.Cond["AllNodesHealthy"].Message, 200))
			npCheck("hcp.np-updating", np.Updating(), "rollout in progress (version=%s, config=%s)",
				condCell(np.Cond, "UpdatingVersion"), condCell(np.Cond, "UpdatingConfig"))
			npCheck("hcp.np-replicas", !np.Autoscaling && np.Current != np.Desired, "%d replica(s) out of %d desired", np.Current, np.Desired)
			npCheck("hcp.np-version-skew", !np.Updating() && np.Version != "" && h.Version != "" && np.Version != h.Version,
				"version %s differs from control plane %s", np.Version, h.Version)

			tNP.AddRow(npLevel(np), np.Namespace+"/"+np.Name, np.Cluster, orDash(np.Version), np.Replicas(),
				condCell(np.Cond, "Ready"), condCell(np.Cond, "AllNodesHealthy"), condCell(np.Cond, "UpdatingVersion"),
				condCell(np.Cond, "UpdatingConfig"), orDash(np.UpgradeType), yesNo(np.AutoRepair))
		}

		cur, want := h.NodeCounts()
		target := "-"
		if h.Desired != "" && h.Desired != h.Version {
			target = h.Desired
		}
		etcd := condCell(c, "EtcdAvailable")
		if h.EtcdTotal > 0 {
			etcd = fmt.Sprintf("%s (%d/%d)", etcd, h.EtcdReady, h.EtcdTotal)
		}
		status := "OK"
		if global != report.Pass {
			status = global.String()
		}
		tHC.AddRow(global, h.Key(), orDash(h.Version), target, orDash(h.HistoryState),
			condCell(c, "Available"), condCell(c, "Degraded"), condCell(c, "Progressing"), etcd,
			condCell(c, "KubeAPIServerAvailable"), fmt.Sprintf("%d/%d", h.CPPods-h.CPNotReady, h.CPPods),
			fmt.Sprintf("%d/%d", h.ReadyPools(), len(h.NodePools)), fmt.Sprintf("%d/%d", cur, want), orDash(h.ACM), status)
		counts[global]++
	}
	s.Summary = fmt.Sprintf("%d HostedCluster(s): %d OK, %d WARN, %d CRIT", len(hcs),
		counts[report.Pass], counts[report.Warn], counts[report.Crit])
	r.done()
	return s
}
