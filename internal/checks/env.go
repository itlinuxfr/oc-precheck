// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

// Package checks implements the pre-upgrade checks, the rule policy
// (severities, exclusions, CEL custom rules) and the maintenance operations
// (Alertmanager silences, console banner, etcd defragmentation, drain,
// live view).
package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/itlinuxfr/oc-upkit/internal/kube"
	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// Env gives checks access to the cluster. Interfaces keep it testable with
// fake clientsets.
type Env struct {
	CS   kubernetes.Interface
	Dyn  dynamic.Interface
	Exec kube.Executor
	// HostedControlPlane is true when the control plane runs outside of the
	// cluster (HyperShift / HCP guest cluster): no etcd pods, no MCP.
	HostedControlPlane bool
	ClusterVersion     string
}

// Options are the tunables exposed as flags and in the config file.
type Options struct {
	Verbose bool
	MaxRows int

	// alerts
	AlertSeverities []string
	IgnoreAlerts    []string
	AlertmanagerURL string
	MonitoringNS    string
	AlertmanagerPod string // optional override

	// etcd
	EtcdNamespace       string
	EtcdFragThreshold   float64 // percent
	EtcdMinDefragSize   int64   // bytes; below it fragmentation is not reported
	EtcdDBSizeWarn      int64   // bytes
	EtcdExpectedMembers int

	// cluster version
	TargetVersion string

	// nodes / pods
	ZoneLabel          string
	GroupByLabels      []string
	NodeSelector       string
	NodePodCapacityPct float64
	PodPendingAge      time.Duration
	PodNotReadyAge     time.Duration
	PodTerminatingAge  time.Duration
	PodRestartWarn     int32

	// quotas / jobs
	QuotaThreshold  float64
	JobLongDuration time.Duration

	// hosted control planes (hub)
	HCPFilter []string // globs on "name" or "namespace/name"

	// Policy holds severity overrides and exclusions; CustomRules the
	// compiled CEL rules from the config file.
	Policy      *Policy
	CustomRules []*CustomRule
}

// DefaultOptions returns the built-in defaults.
func DefaultOptions() *Options {
	return &Options{
		MaxRows:             50,
		AlertSeverities:     []string{"critical", "warning"},
		IgnoreAlerts:        []string{"Watchdog", "AlertmanagerReceiversNotConfigured", "InfoInhibitor"},
		AlertmanagerURL:     "http://localhost:9093",
		MonitoringNS:        "openshift-monitoring",
		EtcdNamespace:       "openshift-etcd",
		EtcdFragThreshold:   45,
		EtcdMinDefragSize:   100 << 20,
		EtcdDBSizeWarn:      6 << 30,
		EtcdExpectedMembers: 3,
		ZoneLabel:           "topology.kubernetes.io/zone",
		NodePodCapacityPct:  90,
		PodPendingAge:       5 * time.Minute,
		PodNotReadyAge:      0,
		PodTerminatingAge:   10 * time.Minute,
		PodRestartWarn:      10,
		QuotaThreshold:      90,
		JobLongDuration:     time.Hour,
		Policy:              &Policy{},
	}
}

// Check is a named read-only verification.
type Check struct {
	Name        string
	Description string
	Run         func(ctx context.Context, env *Env, o *Options) *report.Section
}

// All returns the checks in display order.
func All() []Check {
	return []Check{
		{"clusterversion", "ClusterVersion conditions, channel, update paths, target version", CheckClusterVersion},
		{"alerts", "Firing Alertmanager alerts (critical/warning)", CheckAlerts},
		{"silences", "Active Alertmanager silences (with their UUID)", CheckSilences},
		{"etcd", "etcd health, leader, database size and fragmentation", CheckEtcd},
		{"clusteroperators", "ClusterOperators Available/Degraded/Progressing/Upgradeable", CheckClusterOperators},
		{"olm", "OLM operators: CSVs, Subscriptions, CatalogSources", CheckOLM},
		{"hostedclusters", "HyperShift hub: HostedClusters, NodePools, global status", CheckHostedClusters},
		{"mcp", "MachineConfigPools: paused, updating, nodes outside any pool", CheckMCP},
		{"pdb", "PodDisruptionBudgets violated or blocking drains", CheckPDB},
		{"nodes", "Nodes: status, pods per node / zone / role / label", CheckNodes},
		{"pods", "Unhealthy pods: 0/1 Running, CrashLoop, ImagePull, Pending…", CheckPods},
		{"jobs", "Running Jobs and their duration", CheckJobs},
		{"quotas", "ResourceQuota / ClusterResourceQuota above the usage threshold", CheckQuotas},
		{"csr", "Pending CertificateSigningRequests", CheckCSR},
		{"custom", "Custom CEL rules from the configuration file", CheckCustomRules},
	}
}

// ------------------------------------------------------------------ GVRs

var (
	gvrClusterVersion  = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusterversions"}
	gvrClusterOperator = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusteroperators"}
	gvrInfrastructure  = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "infrastructures"}
	gvrEtcdOperator    = schema.GroupVersionResource{Group: "operator.openshift.io", Version: "v1", Resource: "etcds"}
	gvrMCP             = schema.GroupVersionResource{Group: "machineconfiguration.openshift.io", Version: "v1", Resource: "machineconfigpools"}
	gvrCSV             = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions"}
	gvrSubscription    = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}
	gvrCatalogSource   = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "catalogsources"}
	gvrHostedCluster   = schema.GroupVersionResource{Group: "hypershift.openshift.io", Version: "v1beta1", Resource: "hostedclusters"}
	gvrNodePool        = schema.GroupVersionResource{Group: "hypershift.openshift.io", Version: "v1beta1", Resource: "nodepools"}
	gvrManagedCluster  = schema.GroupVersionResource{Group: "cluster.open-cluster-management.io", Version: "v1", Resource: "managedclusters"}
	gvrClusterQuota    = schema.GroupVersionResource{Group: "quota.openshift.io", Version: "v1", Resource: "clusterresourcequotas"}
	gvrConsoleNotif    = schema.GroupVersionResource{Group: "console.openshift.io", Version: "v1", Resource: "consolenotifications"}
)

var (
	metaGet  = metav1.GetOptions{}
	metaList = metav1.ListOptions{}
)

// Discover fills topology and version information on the Env. Errors are
// ignored: individual checks report their own problems.
func Discover(ctx context.Context, env *Env) map[string]string {
	meta := map[string]string{}
	if infra, err := env.Dyn.Resource(gvrInfrastructure).Get(ctx, "cluster", metaGet); err == nil {
		topo, _, _ := unstructured.NestedString(infra.Object, "status", "controlPlaneTopology")
		plat, _, _ := unstructured.NestedString(infra.Object, "status", "platformStatus", "type")
		meta["platform"] = plat
		if topo == "" {
			topo = "HighlyAvailable"
		}
		if topo == "External" {
			env.HostedControlPlane = true
			topo += " (hosted control plane)"
		}
		meta["topology"] = topo
	}
	if cv, err := env.Dyn.Resource(gvrClusterVersion).Get(ctx, "version", metaGet); err == nil {
		v, _, _ := unstructured.NestedString(cv.Object, "status", "desired", "version")
		ch, _, _ := unstructured.NestedString(cv.Object, "spec", "channel")
		env.ClusterVersion = v
		meta["version"] = v
		meta["channel"] = ch
	}
	return meta
}

// ---------------------------------------------------------------- helpers

type condition struct {
	Type, Status, Reason, Message string
	LastTransition                time.Time
}

// conditions extracts status.conditions from an unstructured object.
func conditions(obj map[string]any) map[string]condition {
	out := map[string]condition{}
	list, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		c := condition{
			Type:    str(m["type"]),
			Status:  str(m["status"]),
			Reason:  str(m["reason"]),
			Message: str(m["message"]),
		}
		if ts := str(m["lastTransitionTime"]); ts != "" {
			c.LastTransition, _ = time.Parse(time.RFC3339, ts)
		}
		out[c.Type] = c
	}
	return out
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func isTrue(c map[string]condition, t string) bool  { return c[t].Status == "True" }
func isFalse(c map[string]condition, t string) bool { return c[t].Status == "False" }

func nestedInt(obj map[string]any, fields ...string) int64 {
	v, found, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	if !found {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// notInstalled reports whether the error means "this API does not exist
// here" (CRD absent), so a check can be skipped instead of failing.
func notInstalled(err error) bool {
	return apierrors.IsNotFound(err) || strings.Contains(err.Error(), "the server could not find the requested resource")
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h >= 24:
		return fmt.Sprintf("%dd%02dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len([]rune(s)) > max {
		return string([]rune(s)[:max-1]) + "…"
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string{}, s[:n]...), fmt.Sprintf("… +%d", len(s)-n))
}
