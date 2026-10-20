// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// RuleInfo documents a built-in rule.
type RuleInfo struct {
	ID          string       `json:"id"`
	Check       string       `json:"check"`
	Level       report.Level `json:"defaultSeverity"`
	Description string       `json:"description"`
}

// Catalog lists every built-in rule. The ID is what the configuration file
// uses to override severities and declare exclusions.
var Catalog = []RuleInfo{
	{"cv.failing", "clusterversion", report.Crit, "ClusterVersion Failing=True"},
	{"cv.unavailable", "clusterversion", report.Crit, "ClusterVersion Available=False"},
	{"cv.progressing", "clusterversion", report.Warn, "an update is already in progress"},
	{"cv.not-upgradeable", "clusterversion", report.Warn, "Upgradeable=False (minor updates blocked)"},
	{"cv.updates-not-retrieved", "clusterversion", report.Warn, "the update service cannot be reached"},
	{"cv.release-not-accepted", "clusterversion", report.Crit, "ReleaseAccepted=False"},
	{"cv.history-partial", "clusterversion", report.Warn, "the last update did not complete"},
	{"cv.overrides", "clusterversion", report.Warn, "unmanaged spec.overrides are set"},
	{"cv.target-conditional", "clusterversion", report.Warn, "the target version is a conditional update"},
	{"cv.target-unknown", "clusterversion", report.Crit, "the target version is not in the update graph"},
	{"cv.minor-blocked", "clusterversion", report.Crit, "minor update requested while Upgradeable=False"},

	{"alerts.critical", "alerts", report.Crit, "firing alert with severity critical"},
	{"alerts.warning", "alerts", report.Warn, "firing alert with severity warning"},
	{"alerts.other", "alerts", report.Info, "firing alert with another severity"},
	{"alerts.suppressed", "alerts", report.Info, "silenced or inhibited alert"},

	{"silences.active", "silences", report.Info, "active Alertmanager silence"},
	{"silences.match-all", "silences", report.Warn, "silence muting every alert"},

	{"etcd.members-degraded", "etcd", report.Crit, "etcd operator EtcdMembersDegraded=True"},
	{"etcd.member-unhealthy", "etcd", report.Crit, "etcd member unhealthy or unreachable"},
	{"etcd.pod-missing", "etcd", report.Warn, "etcd member without an etcd pod"},
	{"etcd.pod-not-ready", "etcd", report.Crit, "etcd pod not Ready"},
	{"etcd.no-leader", "etcd", report.Crit, "no etcd leader"},
	{"etcd.member-count", "etcd", report.Warn, "number of members differs from the expected count"},
	{"etcd.version-mismatch", "etcd", report.Warn, "etcd members run different versions"},
	{"etcd.alarm", "etcd", report.Crit, "active etcd alarm (NOSPACE…)"},
	{"etcd.fragmentation", "etcd", report.Warn, "fragmentation above the threshold"},
	{"etcd.db-size", "etcd", report.Warn, "database size above the threshold"},

	{"co.unavailable", "clusteroperators", report.Crit, "ClusterOperator Available=False"},
	{"co.degraded", "clusteroperators", report.Crit, "ClusterOperator Degraded=True"},
	{"co.progressing", "clusteroperators", report.Warn, "ClusterOperator Progressing=True"},
	{"co.not-upgradeable", "clusteroperators", report.Warn, "ClusterOperator Upgradeable=False"},
	{"co.version-mismatch", "clusteroperators", report.Warn, "operator version differs from the cluster version"},

	{"olm.csv-failed", "olm", report.Crit, "CSV in phase Failed"},
	{"olm.csv-not-succeeded", "olm", report.Warn, "CSV in a phase other than Succeeded"},
	{"olm.sub-resolution-failed", "olm", report.Crit, "Subscription ResolutionFailed"},
	{"olm.sub-installplan-failed", "olm", report.Crit, "Subscription InstallPlanFailed"},
	{"olm.sub-catalog-unhealthy", "olm", report.Warn, "Subscription with unhealthy CatalogSources"},
	{"olm.sub-manual-pending", "olm", report.Warn, "InstallPlan waiting for manual approval"},
	{"olm.sub-upgrade-pending", "olm", report.Warn, "operator update pending"},
	{"olm.sub-no-state", "olm", report.Warn, "Subscription without state"},
	{"olm.catalog-not-ready", "olm", report.Warn, "CatalogSource not READY"},

	{"mcp.degraded", "mcp", report.Crit, "pool degraded"},
	{"mcp.updating", "mcp", report.Warn, "pool update in progress"},
	{"mcp.paused", "mcp", report.Warn, "pool paused"},
	{"mcp.node-orphan", "mcp", report.Warn, "node outside any pool"},
	{"mcp.node-multi-pool", "mcp", report.Warn, "node in several custom pools"},
	{"mcp.node-degraded", "mcp", report.Crit, "machine-config-daemon Degraded on the node"},
	{"mcp.node-working", "mcp", report.Warn, "machine-config-daemon Working on the node"},
	{"mcp.node-config-mismatch", "mcp", report.Warn, "currentConfig differs from desiredConfig"},

	{"pdb.violated", "pdb", report.Warn, "PDB violated (healthy < desired)"},
	{"pdb.blocking", "pdb", report.Warn, "PDB allowing 0 disruptions: will block drains"},
	{"pdb.empty", "pdb", report.Info, "PDB selecting no pod"},

	{"nodes.not-ready", "nodes", report.Crit, "node NotReady"},
	{"nodes.cordoned", "nodes", report.Warn, "node SchedulingDisabled"},
	{"nodes.pressure", "nodes", report.Warn, "Memory/Disk/PID pressure"},
	{"nodes.pod-capacity", "nodes", report.Warn, "pod capacity usage above the threshold"},

	{"pods.not-ready", "pods", report.Warn, "Running pod with containers not ready (0/1…)"},
	{"pods.waiting-error", "pods", report.Warn, "CrashLoopBackOff, ImagePullBackOff, CreateContainerConfigError…"},
	{"pods.pending", "pods", report.Warn, "pod Pending longer than the threshold"},
	{"pods.terminating", "pods", report.Warn, "pod stuck in Terminating"},
	{"pods.unknown", "pods", report.Warn, "pod in phase Unknown"},
	{"pods.failed", "pods", report.Info, "pod in phase Failed (Evicted, Error…)"},
	{"pods.restarts", "pods", report.Info, "high restart count"},

	{"jobs.running", "jobs", report.Info, "running Job"},
	{"jobs.long-running", "jobs", report.Warn, "Job running longer than the threshold"},

	{"quotas.high", "quotas", report.Warn, "quota usage above the threshold on at least one resource"},
	{"quotas.full", "quotas", report.Warn, "quota exhausted (100 %)"},

	{"csr.pending", "csr", report.Warn, "CSR waiting for approval"},

	{"hcp.unavailable", "hostedclusters", report.Crit, "HostedCluster Available≠True"},
	{"hcp.degraded", "hostedclusters", report.Crit, "HostedCluster Degraded=True"},
	{"hcp.etcd-unavailable", "hostedclusters", report.Crit, "EtcdAvailable=False"},
	{"hcp.kas-unavailable", "hostedclusters", report.Crit, "KubeAPIServerAvailable=False"},
	{"hcp.cv-failing", "hostedclusters", report.Crit, "ClusterVersionSucceeding=False"},
	{"hcp.invalid-release", "hostedclusters", report.Crit, "ValidReleaseImage=False"},
	{"hcp.progressing", "hostedclusters", report.Warn, "HostedCluster update in progress"},
	{"hcp.not-upgradeable", "hostedclusters", report.Warn, "ClusterVersionUpgradeable=False"},
	{"hcp.reconciliation-paused", "hostedclusters", report.Warn, "reconciliation paused (pausedUntil)"},
	{"hcp.cp-pods", "hostedclusters", report.Warn, "hosted control plane pods not ready"},
	{"hcp.acm-unavailable", "hostedclusters", report.Warn, "ACM ManagedCluster not Available"},
	{"hcp.np-not-ready", "hostedclusters", report.Crit, "NodePool Ready=False"},
	{"hcp.np-unhealthy-nodes", "hostedclusters", report.Crit, "NodePool AllNodesHealthy=False"},
	{"hcp.np-updating", "hostedclusters", report.Warn, "NodePool UpdatingVersion/UpdatingConfig"},
	{"hcp.np-replicas", "hostedclusters", report.Warn, "NodePool current replicas differ from desired"},
	{"hcp.np-version-skew", "hostedclusters", report.Warn, "NodePool version differs from the control plane"},
}

var catalogIndex = func() map[string]RuleInfo {
	m := map[string]RuleInfo{}
	for _, r := range Catalog {
		m[r.ID] = r
	}
	return m
}()

// Exclusion ignores a rule for some namespaces and/or object names.
// All patterns are shell globs (path.Match).
type Exclusion struct {
	Rule       string   `json:"rule"`
	Namespaces []string `json:"namespaces,omitempty"`
	Names      []string `json:"names,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

func (e Exclusion) matches(rule, ns, name string) bool {
	pattern := e.Rule
	if pattern == "" {
		pattern = "*"
	}
	if ok, _ := path.Match(pattern, rule); !ok {
		return false
	}
	if len(e.Namespaces) > 0 && !globAny(e.Namespaces, ns) {
		return false
	}
	if len(e.Names) > 0 && !globAny(e.Names, name) {
		return false
	}
	return true
}

func globAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, s); ok {
			return true
		}
	}
	return false
}

// Policy holds severity overrides and exclusions from the config file.
type Policy struct {
	Severities map[string]string // rule ID or glob -> crit|warn|info|off
	Exclusions []Exclusion
	custom     map[string]report.Level
}

// ParseLevel converts a configuration severity. The boolean is false when
// the rule is disabled.
func ParseLevel(s string) (report.Level, bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "crit", "critical":
		return report.Crit, true, nil
	case "warn", "warning":
		return report.Warn, true, nil
	case "info":
		return report.Info, true, nil
	// YAML 1.1 turns an unquoted `off` / `no` into the boolean false.
	case "off", "ignore", "disabled", "none", "false", "no":
		return report.Pass, false, nil
	}
	return report.Pass, false, fmt.Errorf("unknown severity %q (crit, warn, info, off)", s)
}

// LevelName is the configuration spelling of a level.
func LevelName(l report.Level) string {
	switch l {
	case report.Crit:
		return "crit"
	case report.Warn:
		return "warn"
	case report.Info:
		return "info"
	}
	return "off"
}

// Validate checks that every key refers to a known rule.
func (p *Policy) Validate() error {
	for k, v := range p.Severities {
		if _, _, err := ParseLevel(v); err != nil {
			return fmt.Errorf("severities.%s: %w", k, err)
		}
		if strings.ContainsAny(k, "*?[") {
			if _, err := path.Match(k, ""); err != nil {
				return fmt.Errorf("severities: invalid pattern %q", k)
			}
			continue
		}
		if _, ok := catalogIndex[k]; !ok {
			if _, ok := p.custom[k]; !ok {
				return fmt.Errorf("severities: unknown rule %q (see `oc upkit rules`)", k)
			}
		}
	}
	for i, e := range p.Exclusions {
		if e.Rule != "" {
			if _, err := path.Match(e.Rule, ""); err != nil {
				return fmt.Errorf("exclusions[%d].rule: invalid pattern %q", i, e.Rule)
			}
		}
	}
	return nil
}

// Effective returns the level of a rule after overrides and whether the
// rule is enabled at all.
func (p *Policy) Effective(rule string) (report.Level, bool) {
	def, ok := catalogIndex[rule]
	lvl := def.Level
	if !ok {
		lvl = report.Warn
		if p != nil {
			if l, found := p.custom[rule]; found {
				lvl = l
			}
		}
	}
	if p == nil || len(p.Severities) == 0 {
		return lvl, true
	}
	if v, ok := p.Severities[rule]; ok {
		l, on, _ := ParseLevel(v)
		return l, on
	}
	// Glob keys: the longest (most specific) matching pattern wins.
	var keys []string
	for k := range p.Severities {
		if strings.ContainsAny(k, "*?[") {
			if ok, _ := path.Match(k, rule); ok {
				keys = append(keys, k)
			}
		}
	}
	if len(keys) > 0 {
		sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
		l, on, _ := ParseLevel(p.Severities[keys[0]])
		return l, on
	}
	return lvl, true
}

// Excluded reports whether an object is excluded for a rule.
func (p *Policy) Excluded(rule, ns, name string) bool {
	if p == nil {
		return false
	}
	for _, e := range p.Exclusions {
		if e.matches(rule, ns, name) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------ ruler

// ruler applies the policy while a check builds its section.
type ruler struct {
	s        *report.Section
	o        *Options
	excluded int
}

func newRuler(s *report.Section, o *Options) *ruler { return &ruler{s: s, o: o} }

func (r *ruler) known(rule string) bool {
	if _, ok := catalogIndex[rule]; ok {
		return true
	}
	if r.o.Policy != nil {
		_, ok := r.o.Policy.custom[rule]
		return ok
	}
	return false
}

// eval returns the effective level for an object and whether to report it.
// Using an uncataloged rule is a programming error caught by the tests.
func (r *ruler) eval(rule, ns, name string) (report.Level, bool) {
	if !r.known(rule) {
		panic("rule not in catalog: " + rule)
	}
	lvl, on := r.o.Policy.Effective(rule)
	if !on {
		return lvl, false
	}
	if r.o.Policy.Excluded(rule, ns, name) {
		r.excluded++
		return lvl, false
	}
	return lvl, true
}

// add records a finding for a rule; it returns the level and whether the
// finding was kept.
func (r *ruler) add(rule, ns, name, format string, args ...any) (report.Level, bool) {
	lvl, ok := r.eval(rule, ns, name)
	if ok {
		r.s.AddRule(rule, lvl, fmt.Sprintf(format, args...))
	}
	return lvl, ok
}

// row adds a table row for a rule violation.
func (r *ruler) row(t *report.Table, rule, ns, name string, cells ...string) (report.Level, bool) {
	lvl, ok := r.eval(rule, ns, name)
	if ok {
		t.AddRow(lvl, cells...)
		r.s.Escalate(lvl)
	}
	return lvl, ok
}

// done adds a note about excluded objects.
func (r *ruler) done() {
	if r.excluded > 0 {
		r.s.Add(report.Info, "%d item(s) excluded by the configuration", r.excluded)
	}
}

// effectiveLevel returns the effective level of a rule (used for summary
// findings whose objects were already filtered).
func effectiveLevel(o *Options, rule string) report.Level {
	l, _ := o.Policy.Effective(rule)
	return l
}
