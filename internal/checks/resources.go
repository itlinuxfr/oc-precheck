// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// ---------------------------------------------------------------- jobs

// CheckJobs lists running Jobs with their duration: draining their node
// interrupts them.
func CheckJobs(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("jobs", "Running Jobs")
	jobs, err := env.CS.BatchV1().Jobs("").List(ctx, metaList)
	if err != nil {
		return s.Fail(err, "")
	}
	r := newRuler(s, o)

	// Nodes running each job, with a single pod list.
	nodesOf := map[string]map[string]bool{}
	for _, key := range []string{"batch.kubernetes.io/job-name", "job-name"} {
		pods, err := listPods(ctx, env, "", true, key)
		if err != nil {
			continue
		}
		for _, p := range pods {
			k := p.Namespace + "/" + p.Labels[key]
			if nodesOf[k] == nil {
				nodesOf[k] = map[string]bool{}
			}
			if p.Spec.NodeName != "" {
				nodesOf[k][p.Spec.NodeName] = true
			}
		}
		if len(pods) > 0 {
			break
		}
	}

	type running struct {
		ns, name, cron, nodes, completions string
		start                              time.Time
		active, failed                     int32
	}
	var list []running
	now := time.Now()
	for _, j := range jobs.Items {
		if j.Status.Active == 0 || j.Status.CompletionTime != nil {
			continue
		}
		cron := "-"
		for _, ref := range j.OwnerReferences {
			if ref.Kind == "CronJob" {
				cron = ref.Name
			}
		}
		start := j.CreationTimestamp.Time
		if j.Status.StartTime != nil {
			start = j.Status.StartTime.Time
		}
		comp := "-"
		if j.Spec.Completions != nil {
			comp = fmt.Sprintf("%d/%d", j.Status.Succeeded, *j.Spec.Completions)
		}
		list = append(list, running{
			ns: j.Namespace, name: j.Name, cron: cron, start: start, completions: comp,
			active: j.Status.Active, failed: j.Status.Failed,
			nodes: strings.Join(sortedKeys(nodesOf[j.Namespace+"/"+j.Name]), ","),
		})
	}
	sort.Slice(list, func(i, k int) bool { return list[i].start.Before(list[k].start) })

	t := s.NewTable("", "Namespace", "Job", "CronJob", "Started", "Duration", "Active", "Completions", "Failed", "Nodes")
	var long int
	for _, j := range list {
		d := now.Sub(j.start)
		rule := "jobs.running"
		if o.JobLongDuration > 0 && d > o.JobLongDuration {
			rule = "jobs.long-running"
		}
		if lvl, kept := r.row(t, rule, j.ns, j.name, j.ns, j.name, j.cron, j.start.Local().Format("2006-01-02 15:04"),
			humanDuration(d), fmt.Sprint(j.active), j.completions, fmt.Sprint(j.failed), orDash(j.nodes)); kept && lvl >= report.Warn {
			long++
		}
	}
	t.Limit(o.MaxRows)

	activeCron := 0
	if cj, err := env.CS.BatchV1().CronJobs("").List(ctx, metaList); err == nil {
		for _, c := range cj.Items {
			if c.Spec.Suspend == nil || !*c.Spec.Suspend {
				activeCron++
			}
		}
	}
	s.Summary = fmt.Sprintf("%d running Job(s), %d running for more than %s — %d CronJob(s) not suspended",
		len(list), long, fmtDuration(o.JobLongDuration), activeCron)
	if len(list) > 0 {
		s.Add(report.Info, "Running Jobs are interrupted when their node is drained: schedule the upgrade after they complete when possible")
	}
	r.done()
	return s
}

// -------------------------------------------------------------- quotas

type quotaUse struct {
	resource   string
	used, hard string
	pct        float64
}

func quotaUsage(hard, used corev1.ResourceList) []quotaUse {
	var out []quotaUse
	for name, h := range hard {
		u := used[name]
		hv, uv := h.AsApproximateFloat64(), u.AsApproximateFloat64()
		var p float64
		switch {
		case hv > 0:
			p = uv * 100 / hv
		case uv > 0:
			p = 100
		default:
			continue
		}
		out = append(out, quotaUse{string(name), u.String(), h.String(), p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pct > out[j].pct })
	return out
}

func parseResourceList(m map[string]any) corev1.ResourceList {
	out := corev1.ResourceList{}
	for k, v := range m {
		if q, err := resource.ParseQuantity(str(v)); err == nil {
			out[corev1.ResourceName(k)] = q
		}
	}
	return out
}

// CheckQuotas lists ResourceQuotas and ClusterResourceQuotas whose usage
// reaches the threshold on at least one resource.
func CheckQuotas(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("quotas", fmt.Sprintf("Quotas used at ≥ %.0f%%", o.QuotaThreshold))
	rq, err := env.CS.CoreV1().ResourceQuotas("").List(ctx, metaList)
	if err != nil {
		return s.Fail(err, "")
	}
	r := newRuler(s, o)
	t := s.NewTable("", "Kind", "Namespace", "Quota", "Resource", "Used", "Hard", "%")
	var nQuotas, nOver int
	handle := func(kind, ns, name string, uses []quotaUse) {
		nQuotas++
		over := false
		for _, u := range uses {
			if u.pct < o.QuotaThreshold {
				continue
			}
			rule := "quotas.high"
			if u.pct >= 100 {
				rule = "quotas.full"
			}
			if _, kept := r.row(t, rule, ns, name, kind, orDash(ns), name, u.resource, u.used, u.hard, fmt.Sprintf("%.0f%%", u.pct)); kept {
				over = true
			}
		}
		if over {
			nOver++
		} else if o.Verbose && len(uses) > 0 {
			u := uses[0]
			t.AddRow(report.Pass, kind, orDash(ns), name, u.resource+" (max)", u.used, u.hard, fmt.Sprintf("%.0f%%", u.pct))
		}
	}
	items := rq.Items
	sort.Slice(items, func(i, j int) bool {
		if items[i].Namespace != items[j].Namespace {
			return items[i].Namespace < items[j].Namespace
		}
		return items[i].Name < items[j].Name
	})
	for _, q := range items {
		handle("ResourceQuota", q.Namespace, q.Name, quotaUsage(q.Status.Hard, q.Status.Used))
	}
	if crq, err := env.Dyn.Resource(gvrClusterQuota).List(ctx, metaList); err == nil {
		for _, q := range crq.Items {
			hard, _, _ := unstructured.NestedMap(q.Object, "status", "total", "hard")
			used, _, _ := unstructured.NestedMap(q.Object, "status", "total", "used")
			nsList, _, _ := unstructured.NestedSlice(q.Object, "status", "namespaces")
			handle("ClusterResourceQuota", fmt.Sprintf("(%d ns)", len(nsList)), q.GetName(),
				quotaUsage(parseResourceList(hard), parseResourceList(used)))
		}
	}
	t.Limit(o.MaxRows)
	s.Summary = fmt.Sprintf("%d quota(s) analyzed, %d at or above %.0f%% on at least one resource", nQuotas, nOver, o.QuotaThreshold)
	if nOver > 0 {
		s.Add(report.Info, "An exhausted quota can prevent pods from being recreated during a drain (Deployment maxSurge, new StatefulSet pods)")
	}
	r.done()
	return s
}
