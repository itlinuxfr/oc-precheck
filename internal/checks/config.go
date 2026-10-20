// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// Duration accepts Go duration strings ("90s", "15m", "2h") in YAML.
type Duration struct{ time.Duration }

// UnmarshalJSON parses a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"15m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalJSON renders the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(fmtDuration(d.Duration)) }

// Settings mirrors the command-line flags. Each field is applied only when
// the corresponding flag was not set explicitly.
type Settings struct {
	Checks              []string  `json:"checks,omitempty"`
	Skip                []string  `json:"skip,omitempty"`
	FailOn              string    `json:"failOn,omitempty"`
	MaxRows             *int      `json:"maxRows,omitempty"`
	AlertSeverities     []string  `json:"alertSeverities,omitempty"`
	IgnoreAlerts        []string  `json:"ignoreAlerts,omitempty"`
	AlertmanagerURL     string    `json:"alertmanagerURL,omitempty"`
	MonitoringNamespace string    `json:"monitoringNamespace,omitempty"`
	EtcdNamespace       string    `json:"etcdNamespace,omitempty"`
	EtcdFragThreshold   *float64  `json:"etcdFragThreshold,omitempty"`
	EtcdDBSizeWarn      string    `json:"etcdDBSizeWarn,omitempty"`
	EtcdExpectedMembers *int      `json:"etcdExpectedMembers,omitempty"`
	TargetVersion       string    `json:"targetVersion,omitempty"`
	ZoneLabel           string    `json:"zoneLabel,omitempty"`
	GroupByLabels       []string  `json:"groupByLabels,omitempty"`
	NodeSelector        string    `json:"nodeSelector,omitempty"`
	NodePodCapacityPct  *float64  `json:"nodePodCapacityPct,omitempty"`
	PodPendingAge       *Duration `json:"podPendingAge,omitempty"`
	PodNotReadyAge      *Duration `json:"podNotReadyAge,omitempty"`
	PodTerminatingAge   *Duration `json:"podTerminatingAge,omitempty"`
	PodRestartWarn      *int32    `json:"podRestartWarn,omitempty"`
	QuotaThreshold      *float64  `json:"quotaThreshold,omitempty"`
	JobLongDuration     *Duration `json:"jobLongDuration,omitempty"`
	HCPFilter           []string  `json:"hcpFilter,omitempty"`
}

// Config is the YAML configuration file.
type Config struct {
	Settings    Settings          `json:"settings,omitempty"`
	Severities  map[string]string `json:"severities,omitempty"`
	Exclusions  []Exclusion       `json:"exclusions,omitempty"`
	CustomRules []CustomRuleSpec  `json:"customRules,omitempty"`
}

// ParseConfig decodes a configuration document. Unknown keys are rejected
// to catch typos.
func ParseConfig(raw []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// LoadConfig reads and decodes a configuration file.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := ParseConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Apply loads the configuration into the options. changed(flag) reports
// whether a command-line flag was set explicitly: explicit flags win.
func (c *Config) Apply(o *Options, changed func(flag string) bool) error {
	s := c.Settings
	set := func(flag string, cond bool, fn func()) {
		if cond && !changed(flag) {
			fn()
		}
	}
	set("max-rows", s.MaxRows != nil, func() { o.MaxRows = *s.MaxRows })
	set("alert-severities", len(s.AlertSeverities) > 0, func() { o.AlertSeverities = s.AlertSeverities })
	set("ignore-alerts", len(s.IgnoreAlerts) > 0, func() { o.IgnoreAlerts = s.IgnoreAlerts })
	set("alertmanager-url", s.AlertmanagerURL != "", func() { o.AlertmanagerURL = s.AlertmanagerURL })
	set("monitoring-namespace", s.MonitoringNamespace != "", func() { o.MonitoringNS = s.MonitoringNamespace })
	set("etcd-namespace", s.EtcdNamespace != "", func() { o.EtcdNamespace = s.EtcdNamespace })
	set("etcd-frag-threshold", s.EtcdFragThreshold != nil, func() { o.EtcdFragThreshold = *s.EtcdFragThreshold })
	set("etcd-expected-members", s.EtcdExpectedMembers != nil, func() { o.EtcdExpectedMembers = *s.EtcdExpectedMembers })
	set("target-version", s.TargetVersion != "", func() { o.TargetVersion = s.TargetVersion })
	set("zone-label", s.ZoneLabel != "", func() { o.ZoneLabel = s.ZoneLabel })
	set("group-by-label", len(s.GroupByLabels) > 0, func() { o.GroupByLabels = s.GroupByLabels })
	set("node-selector", s.NodeSelector != "", func() { o.NodeSelector = s.NodeSelector })
	set("node-pod-capacity", s.NodePodCapacityPct != nil, func() { o.NodePodCapacityPct = *s.NodePodCapacityPct })
	set("pod-pending-age", s.PodPendingAge != nil, func() { o.PodPendingAge = s.PodPendingAge.Duration })
	set("pod-notready-age", s.PodNotReadyAge != nil, func() { o.PodNotReadyAge = s.PodNotReadyAge.Duration })
	set("pod-terminating-age", s.PodTerminatingAge != nil, func() { o.PodTerminatingAge = s.PodTerminatingAge.Duration })
	set("pod-restart-warn", s.PodRestartWarn != nil, func() { o.PodRestartWarn = *s.PodRestartWarn })
	set("quota-threshold", s.QuotaThreshold != nil, func() { o.QuotaThreshold = *s.QuotaThreshold })
	set("job-long-duration", s.JobLongDuration != nil, func() { o.JobLongDuration = s.JobLongDuration.Duration })
	set("hcp-filter", len(s.HCPFilter) > 0, func() { o.HCPFilter = s.HCPFilter })
	if s.EtcdDBSizeWarn != "" && !changed("etcd-db-size-warn") {
		q, err := resource.ParseQuantity(s.EtcdDBSizeWarn)
		if err != nil {
			return fmt.Errorf("settings.etcdDBSizeWarn: %w", err)
		}
		o.EtcdDBSizeWarn = q.Value()
	}
	if s.FailOn != "" && s.FailOn != "warn" && s.FailOn != "crit" {
		return fmt.Errorf("settings.failOn must be warn or crit")
	}

	if o.Policy == nil {
		o.Policy = &Policy{}
	}
	if o.Policy.Severities == nil {
		o.Policy.Severities = map[string]string{}
	}
	for k, v := range c.Severities {
		o.Policy.Severities[k] = v
	}
	o.Policy.Exclusions = append(o.Policy.Exclusions, c.Exclusions...)

	rules, err := CompileCustomRules(c.CustomRules)
	if err != nil {
		return err
	}
	if o.Policy.custom == nil {
		o.Policy.custom = map[string]report.Level{}
	}
	for _, r := range rules {
		if _, dup := catalogIndex[r.ID]; dup {
			return fmt.Errorf("customRules: %q is already a built-in rule", r.ID)
		}
		if _, dup := o.Policy.custom[r.ID]; dup {
			return fmt.Errorf("customRules: duplicate id %q", r.ID)
		}
		o.Policy.custom[r.ID] = r.Level
	}
	o.CustomRules = append(o.CustomRules, rules...)
	return o.Policy.Validate()
}

// ExcludeNamespaces adds an exclusion, used by shortcut flags such as
// --pdb-ignore-namespaces.
func (o *Options) ExcludeNamespaces(rulePattern string, namespaces []string, reason string) {
	if len(namespaces) == 0 {
		return
	}
	if o.Policy == nil {
		o.Policy = &Policy{}
	}
	o.Policy.Exclusions = append(o.Policy.Exclusions, Exclusion{Rule: rulePattern, Namespaces: namespaces, Reason: reason})
}

// HCPSelected reports whether a hosted cluster passes --hcp-filter.
func (o *Options) HCPSelected(ns, name string) bool {
	if len(o.HCPFilter) == 0 {
		return true
	}
	return globAny(o.HCPFilter, name) || globAny(o.HCPFilter, ns+"/"+name)
}

func fmtDuration(d time.Duration) string {
	s := d.String()
	for _, suffix := range []string{"m0s", "h0m"} {
		if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
			s = s[:len(s)-len(suffix)+1]
		}
	}
	return s
}
