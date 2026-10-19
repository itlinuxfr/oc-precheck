// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/itlinuxfr/oc-precheck/internal/report"
)

// CheckClusterVersion inspects the ClusterVersion object and validates the
// optional target version against the update graph.
func CheckClusterVersion(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("clusterversion", "ClusterVersion and update path")
	cv, err := env.Dyn.Resource(gvrClusterVersion).Get(ctx, "version", metaGet)
	if err != nil {
		if notInstalled(err) {
			return s.Skipped("config.openshift.io API not served: not an OpenShift cluster?")
		}
		return s.Fail(err, "")
	}
	r := newRuler(s, o)
	obj := cv.Object
	current, _, _ := unstructured.NestedString(obj, "status", "desired", "version")
	channel, _, _ := unstructured.NestedString(obj, "spec", "channel")
	clusterID, _, _ := unstructured.NestedString(obj, "spec", "clusterID")
	s.Summary = fmt.Sprintf("Version %s — channel %q — clusterID %s", current, channel, clusterID)

	c := conditions(obj)
	if isTrue(c, "Failing") {
		r.add("cv.failing", "", "version", "ClusterVersion Failing=True: %s", oneLine(c["Failing"].Message, 300))
	}
	if isFalse(c, "Available") {
		r.add("cv.unavailable", "", "version", "ClusterVersion Available=False: %s", oneLine(c["Available"].Message, 300))
	}
	if isTrue(c, "Progressing") {
		r.add("cv.progressing", "", "version", "An update is already in progress: %s", oneLine(c["Progressing"].Message, 300))
	}
	if isFalse(c, "Upgradeable") {
		r.add("cv.not-upgradeable", "", "version", "Upgradeable=False (%s): minor (4.y) updates are blocked — %s",
			c["Upgradeable"].Reason, oneLine(c["Upgradeable"].Message, 400))
	}
	if isFalse(c, "RetrievedUpdates") {
		r.add("cv.updates-not-retrieved", "", "version", "RetrievedUpdates=False (%s): the update service cannot be reached — %s",
			c["RetrievedUpdates"].Reason, oneLine(c["RetrievedUpdates"].Message, 200))
	}
	if x, ok := c["ReleaseAccepted"]; ok && x.Status == "False" {
		r.add("cv.release-not-accepted", "", "version", "ReleaseAccepted=False: %s", oneLine(x.Message, 300))
	}
	if hist, _, _ := unstructured.NestedSlice(obj, "status", "history"); len(hist) > 0 {
		if h, ok := hist[0].(map[string]any); ok && str(h["state"]) != "Completed" {
			r.add("cv.history-partial", "", "version", "Last history entry %s is %q (update not completed)", str(h["version"]), str(h["state"]))
		}
	}
	if ov, _, _ := unstructured.NestedSlice(obj, "spec", "overrides"); len(ov) > 0 {
		var names []string
		for _, item := range ov {
			if m, ok := item.(map[string]any); ok && m["unmanaged"] == true {
				names = append(names, fmt.Sprintf("%s/%s/%s", str(m["kind"]), str(m["namespace"]), str(m["name"])))
			}
		}
		if len(names) > 0 {
			r.add("cv.overrides", "", "version", "Unmanaged spec.overrides are set — they block minor updates: %s", strings.Join(names, ", "))
		}
	}

	avail := map[string]bool{}
	var versions []string
	updates, _, _ := unstructured.NestedSlice(obj, "status", "availableUpdates")
	for _, u := range updates {
		if m, ok := u.(map[string]any); ok {
			v := str(m["version"])
			avail[v] = true
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return semverLess(versions[j], versions[i]) })

	type condUpdate struct {
		version string
		risks   []string
	}
	var conditional []condUpdate
	cu, _, _ := unstructured.NestedSlice(obj, "status", "conditionalUpdates")
	for _, u := range cu {
		m, ok := u.(map[string]any)
		if !ok {
			continue
		}
		v, _, _ := unstructured.NestedString(m, "release", "version")
		var risks []string
		rl, _, _ := unstructured.NestedSlice(m, "risks")
		for _, x := range rl {
			if rm, ok := x.(map[string]any); ok {
				risks = append(risks, str(rm["name"]))
			}
		}
		conditional = append(conditional, condUpdate{v, risks})
	}
	sort.Slice(conditional, func(i, j int) bool { return semverLess(conditional[j].version, conditional[i].version) })

	t := s.NewTable("Updates offered by the update service", "Version", "Type", "Risks")
	for _, v := range versions {
		t.AddRow(report.Pass, v, "recommended", "-")
	}
	for _, x := range conditional {
		t.AddRow(report.Warn, x.version, "conditional", strings.Join(x.risks, ", "))
	}
	t.Limit(o.MaxRows)
	if len(versions) == 0 && len(conditional) == 0 {
		s.Add(report.Info, "No update offered on channel %q", channel)
	} else {
		s.Add(report.Info, "%d recommended update(s), %d conditional", len(versions), len(conditional))
	}

	if tv := o.TargetVersion; tv != "" {
		switch {
		case tv == current:
			s.Add(report.Info, "Target version %s is the current version", tv)
		case avail[tv]:
			s.Add(report.Pass, "Target version %s: recommended path from %s ✓", tv, current)
		default:
			found := false
			for _, x := range conditional {
				if x.version == tv {
					found = true
					r.add("cv.target-conditional", "", tv, "Target version %s is a CONDITIONAL update, risks to assess: %s", tv, strings.Join(x.risks, ", "))
				}
			}
			if !found {
				r.add("cv.target-unknown", "", tv, "Target version %s is not offered from %s on channel %q (check the channel / update graph)", tv, current, channel)
			}
		}
		if minorOf(tv) > minorOf(current) && isFalse(c, "Upgradeable") {
			r.add("cv.minor-blocked", "", tv, "Minor update to %s requested while Upgradeable=False", tv)
		}
	}
	r.done()
	return s
}

// CheckClusterOperators reports unhealthy ClusterOperators.
func CheckClusterOperators(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("clusteroperators", "ClusterOperators")
	list, err := env.Dyn.Resource(gvrClusterOperator).List(ctx, metaList)
	if err != nil {
		if notInstalled(err) {
			return s.Skipped("ClusterOperator API not served")
		}
		return s.Fail(err, "")
	}
	r := newRuler(s, o)
	t := s.NewTable("", "Name", "Version", "Available", "Progressing", "Degraded", "Upgradeable", "Since", "Message")
	var bad int
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
	for _, co := range items {
		c := conditions(co.Object)
		version := ""
		vs, _, _ := unstructured.NestedSlice(co.Object, "status", "versions")
		for _, v := range vs {
			if m, ok := v.(map[string]any); ok && str(m["name"]) == "operator" {
				version = str(m["version"])
			}
		}
		rule, msg, since := "", "", condition{}
		switch {
		case c["Available"].Status != "True":
			rule, msg, since = "co.unavailable", c["Available"].Message, c["Available"]
		case isTrue(c, "Degraded"):
			rule, msg, since = "co.degraded", c["Degraded"].Message, c["Degraded"]
		case isTrue(c, "Progressing"):
			rule, msg, since = "co.progressing", c["Progressing"].Message, c["Progressing"]
		case isFalse(c, "Upgradeable"):
			rule, msg, since = "co.not-upgradeable", c["Upgradeable"].Message, c["Upgradeable"]
		case env.ClusterVersion != "" && version != "" && version != env.ClusterVersion:
			rule, msg = "co.version-mismatch", fmt.Sprintf("operator version %s differs from cluster version %s", version, env.ClusterVersion)
		}
		cells := []string{co.GetName(), version, c["Available"].Status, c["Progressing"].Status, c["Degraded"].Status,
			orDash(c["Upgradeable"].Status), age(since.LastTransition), oneLine(msg, 160)}
		if rule != "" {
			if _, kept := r.row(t, rule, "", co.GetName(), cells...); kept {
				bad++
				continue
			}
		}
		if o.Verbose {
			t.AddRow(report.Pass, cells...)
		}
	}
	t.Limit(o.MaxRows)
	if bad == 0 {
		s.Summary = fmt.Sprintf("%d/%d ClusterOperators healthy", len(items), len(items))
	} else {
		s.Summary = fmt.Sprintf("%d/%d ClusterOperators need attention", bad, len(items))
	}
	r.done()
	return s
}

// CheckOLM reports OLM operator issues (CSV, Subscription, CatalogSource).
func CheckOLM(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("olm", "OLM operators (CSVs, Subscriptions, CatalogSources)")
	csvs, err := env.Dyn.Resource(gvrCSV).Namespace("").List(ctx, metaList)
	if err != nil {
		if notInstalled(err) {
			return s.Skipped("OLM not installed (operators.coreos.com API not served)")
		}
		return s.Fail(err, "")
	}
	r := newRuler(s, o)
	tCSV := s.NewTable("ClusterServiceVersions", "Namespace", "CSV", "Phase", "Reason", "Message")
	var nCSV, badCSV, badSub int
	for _, c := range csvs.Items {
		if _, copied := c.GetLabels()["olm.copiedFrom"]; copied {
			continue // global operators are copied into every namespace
		}
		nCSV++
		phase, _, _ := unstructured.NestedString(c.Object, "status", "phase")
		reason, _, _ := unstructured.NestedString(c.Object, "status", "reason")
		msg, _, _ := unstructured.NestedString(c.Object, "status", "message")
		cells := []string{c.GetNamespace(), c.GetName(), phase, reason, oneLine(msg, 140)}
		rule := ""
		switch phase {
		case "Succeeded":
		case "Failed":
			rule = "olm.csv-failed"
		default:
			rule = "olm.csv-not-succeeded"
		}
		if rule != "" {
			if _, kept := r.row(tCSV, rule, c.GetNamespace(), c.GetName(), cells...); kept {
				badCSV++
				continue
			}
		}
		if o.Verbose {
			tCSV.AddRow(report.Pass, cells...)
		}
	}
	tCSV.Limit(o.MaxRows)

	tSub := s.NewTable("Subscriptions", "Namespace", "Package", "Channel", "Approval", "State", "Installed", "Problem")
	if subs, err := env.Dyn.Resource(gvrSubscription).Namespace("").List(ctx, metaList); err == nil {
		for _, sub := range subs.Items {
			pkg, _, _ := unstructured.NestedString(sub.Object, "spec", "name")
			ch, _, _ := unstructured.NestedString(sub.Object, "spec", "channel")
			appr, _, _ := unstructured.NestedString(sub.Object, "spec", "installPlanApproval")
			state, _, _ := unstructured.NestedString(sub.Object, "status", "state")
			installed, _, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")
			c := conditions(sub.Object)
			rule, problem := "", ""
			switch {
			case isTrue(c, "ResolutionFailed"):
				rule, problem = "olm.sub-resolution-failed", "ResolutionFailed: "+oneLine(c["ResolutionFailed"].Message, 120)
			case isTrue(c, "InstallPlanFailed"):
				rule, problem = "olm.sub-installplan-failed", "InstallPlanFailed: "+oneLine(c["InstallPlanFailed"].Message, 120)
			case isTrue(c, "CatalogSourcesUnhealthy"):
				rule, problem = "olm.sub-catalog-unhealthy", "unhealthy CatalogSource(s)"
			case state == "UpgradePending" && strings.EqualFold(appr, "Manual"):
				rule, problem = "olm.sub-manual-pending", "InstallPlan waiting for manual approval"
			case state == "UpgradePending":
				rule, problem = "olm.sub-upgrade-pending", "update pending"
			case state == "":
				rule, problem = "olm.sub-no-state", "no state (subscription not resolved)"
			}
			cells := []string{sub.GetNamespace(), pkg, ch, appr, state, installed, problem}
			if rule != "" {
				if _, kept := r.row(tSub, rule, sub.GetNamespace(), sub.GetName(), cells...); kept {
					badSub++
					continue
				}
			}
			if o.Verbose {
				tSub.AddRow(report.Pass, cells...)
			}
		}
	} else if !notInstalled(err) {
		s.Add(report.Error, "cannot list Subscriptions: %v", err)
	}
	tSub.Limit(o.MaxRows)

	tCat := s.NewTable("CatalogSources", "Namespace", "Name", "Connection state", "Last connect")
	if cats, err := env.Dyn.Resource(gvrCatalogSource).Namespace("").List(ctx, metaList); err == nil {
		for _, cat := range cats.Items {
			st, _, _ := unstructured.NestedString(cat.Object, "status", "connectionState", "lastObservedState")
			last, _, _ := unstructured.NestedString(cat.Object, "status", "connectionState", "lastConnect")
			cells := []string{cat.GetNamespace(), cat.GetName(), orDash(st), orDash(last)}
			if st != "READY" {
				if _, kept := r.row(tCat, "olm.catalog-not-ready", cat.GetNamespace(), cat.GetName(), cells...); kept {
					continue
				}
			}
			if o.Verbose {
				tCat.AddRow(report.Pass, cells...)
			}
		}
	}

	s.Summary = fmt.Sprintf("%d CSV(s) (%d unhealthy), %d Subscription(s) need attention", nCSV, badCSV, badSub)
	s.Add(report.Info, "Check each operator's compatibility with the target version (Red Hat Operator Update Information Checker)")
	r.done()
	return s
}

// ------------------------------------------------------------- versions

func versionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// semverLess compares x.y.z versions numerically (pre-release ignored).
func semverLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return a < b
}

func minorOf(v string) int {
	p := versionParts(v)
	if len(p) < 2 {
		return 0
	}
	return p[0]*1000 + p[1]
}
