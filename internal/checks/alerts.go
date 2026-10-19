// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/itlinuxfr/oc-precheck/internal/report"
)

// amAlert is the subset of `amtool alert query -o json` used here.
type amAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	Status      struct {
		State       string   `json:"state"`
		SilencedBy  []string `json:"silencedBy"`
		InhibitedBy []string `json:"inhibitedBy"`
	} `json:"status"`
}

// Silence is an Alertmanager silence as returned by amtool.
type Silence struct {
	ID        string    `json:"id"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	Status    struct {
		State string `json:"state"`
	} `json:"status"`
	Matchers []struct {
		Name    string `json:"name"`
		Value   string `json:"value"`
		IsRegex bool   `json:"isRegex"`
		IsEqual *bool  `json:"isEqual"`
	} `json:"matchers"`
}

// MatchersString renders the matchers like amtool does.
func (x Silence) MatchersString() string {
	var p []string
	for _, m := range x.Matchers {
		eq := m.IsEqual == nil || *m.IsEqual
		op := "="
		switch {
		case m.IsRegex && eq:
			op = "=~"
		case m.IsRegex:
			op = "!~"
		case !eq:
			op = "!="
		}
		p = append(p, m.Name+op+m.Value)
	}
	return strings.Join(p, ",")
}

// MatchesAll reports whether the silence mutes every alert.
func (x Silence) MatchesAll() bool {
	if len(x.Matchers) != 1 {
		return false
	}
	m := x.Matchers[0]
	eq := m.IsEqual == nil || *m.IsEqual
	return m.IsRegex && eq && (m.Value == ".+" || m.Value == ".*") && m.Name == "alertname"
}

// alertmanagerPod returns a running Alertmanager pod. amtool inside the
// "alertmanager" container talks to Alertmanager on localhost, which avoids
// depending on the kubeconfig authentication method (token vs certificate).
func alertmanagerPod(ctx context.Context, env *Env, o *Options) (string, error) {
	if o.AlertmanagerPod != "" {
		return o.AlertmanagerPod, nil
	}
	pods, err := env.CS.CoreV1().Pods(o.MonitoringNS).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=alertmanager",
	})
	if err != nil {
		return "", err
	}
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	for _, p := range pods.Items {
		if strings.HasPrefix(p.Name, "alertmanager-main") && p.Status.Phase == corev1.PodRunning {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no running alertmanager-main pod in %s", o.MonitoringNS)
}

func amtool(ctx context.Context, env *Env, o *Options, pod string, args ...string) (string, error) {
	cmd := append([]string{"amtool", "--alertmanager.url=" + o.AlertmanagerURL}, args...)
	out, _, err := env.Exec.Exec(ctx, o.MonitoringNS, pod, "alertmanager", cmd)
	return out, err
}

// CheckAlerts lists firing alerts by severity; silenced and inhibited
// alerts are listed for information.
func CheckAlerts(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("alerts", "Firing alerts (Alertmanager)")
	pod, err := alertmanagerPod(ctx, env, o)
	if err != nil {
		return s.Fail(err, "is the monitoring stack deployed?")
	}
	out, err := amtool(ctx, env, o, pod, "alert", "query", "--silenced", "--inhibited", "-o", "json")
	if err != nil {
		return s.Fail(err, "pods/exec permission required in "+o.MonitoringNS)
	}
	var alerts []amAlert
	if err := json.Unmarshal([]byte(out), &alerts); err != nil {
		return s.Fail(fmt.Errorf("decoding amtool output: %w", err), "")
	}
	r := newRuler(s, o)

	sevRank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(alerts, func(i, j int) bool {
		ai, aj := alerts[i].Status.State == "active", alerts[j].Status.State == "active"
		if ai != aj {
			return ai
		}
		ri, rj := rank(sevRank, alerts[i].Labels["severity"]), rank(sevRank, alerts[j].Labels["severity"])
		if ri != rj {
			return ri < rj
		}
		return alerts[i].StartsAt.Before(alerts[j].StartsAt)
	})

	t := s.NewTable("", "Severity", "State", "Alert", "Namespace", "Since", "Summary")
	counts := map[string]int{}
	suppressed := 0
	for _, a := range alerts {
		name := a.Labels["alertname"]
		sev := a.Labels["severity"]
		if contains(o.IgnoreAlerts, name) || !contains(o.AlertSeverities, sev) {
			continue
		}
		summary := firstNonEmpty(a.Annotations["summary"], a.Annotations["message"], a.Annotations["description"])
		state, rule := "active", "alerts.other"
		switch {
		case a.Status.State != "active":
			state, rule = "silenced", "alerts.suppressed"
			if len(a.Status.InhibitedBy) > 0 && len(a.Status.SilencedBy) == 0 {
				state = "inhibited"
			}
		case sev == "critical":
			rule = "alerts.critical"
		case sev == "warning":
			rule = "alerts.warning"
		}
		if _, kept := r.row(t, rule, a.Labels["namespace"], name, sev, state, name, orDash(a.Labels["namespace"]),
			age(a.StartsAt), oneLine(summary, 110)); kept {
			if state == "active" {
				counts[sev]++
			} else {
				suppressed++
			}
		}
	}
	t.Limit(o.MaxRows)

	var parts []string
	for _, k := range sortedKeys(counts) {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	if len(parts) == 0 {
		s.Summary = fmt.Sprintf("No firing alert with severity %s", strings.Join(o.AlertSeverities, "/"))
	} else {
		s.Summary = "Firing alerts: " + strings.Join(parts, ", ")
	}
	if suppressed > 0 {
		s.Summary += fmt.Sprintf(" — %d silenced/inhibited", suppressed)
	}
	r.done()
	return s
}

func rank(m map[string]int, k string) int {
	if r, ok := m[k]; ok {
		return r
	}
	return 99
}

// ListSilences returns the active and pending silences.
func ListSilences(ctx context.Context, env *Env, o *Options) ([]Silence, error) {
	pod, err := alertmanagerPod(ctx, env, o)
	if err != nil {
		return nil, err
	}
	out, err := amtool(ctx, env, o, pod, "silence", "query", "-o", "json")
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var sil []Silence
	if err := json.Unmarshal([]byte(out), &sil); err != nil {
		return nil, fmt.Errorf("decoding silences: %w", err)
	}
	sort.Slice(sil, func(i, j int) bool { return sil[i].EndsAt.Before(sil[j].EndsAt) })
	return sil, nil
}

// CheckSilences lists the active silences with their full UUID.
func CheckSilences(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("silences", "Alertmanager silences")
	sil, err := ListSilences(ctx, env, o)
	if err != nil {
		return s.Fail(err, "pods/exec permission required in "+o.MonitoringNS)
	}
	r := newRuler(s, o)
	if len(sil) == 0 {
		s.Summary = "No active silence"
		return s
	}
	t := s.NewTable("", "UUID", "State", "Author", "Starts", "Ends", "Remaining", "Matchers", "Comment")
	for _, x := range sil {
		rule := "silences.active"
		if x.MatchesAll() {
			rule = "silences.match-all"
		}
		remaining := "-"
		if d := time.Until(x.EndsAt); d > 0 {
			remaining = humanDuration(d)
		}
		r.row(t, rule, "", x.ID, x.ID, x.Status.State, x.CreatedBy,
			x.StartsAt.Local().Format("2006-01-02 15:04"), x.EndsAt.Local().Format("2006-01-02 15:04"), remaining,
			x.MatchersString(), oneLine(x.Comment, 50))
		if x.MatchesAll() {
			r.add("silences.match-all", "", x.ID, "Silence %s mutes ALL alerts until %s", x.ID, x.EndsAt.Local().Format("2006-01-02 15:04"))
		}
	}
	s.Summary = fmt.Sprintf("%d active silence(s) — expire them after the maintenance: oc precheck silence expire <UUID>", len(sil))
	r.done()
	return s
}

// ------------------------------------------------------------- actions

// SilenceOptions configures the Alertmanager silence action.
type SilenceOptions struct {
	Duration time.Duration
	Author   string
	Comment  string
	Matchers []string
	DryRun   bool
}

// ActionSilence creates an Alertmanager silence for the maintenance window.
func ActionSilence(ctx context.Context, env *Env, o *Options, so SilenceOptions) *report.Section {
	s := report.NewSection("silence", "Action: Alertmanager silence")
	matchers := so.Matchers
	if len(matchers) == 0 {
		matchers = []string{"alertname=~.+"}
	}
	end := time.Now().Add(so.Duration)
	s.Summary = fmt.Sprintf("Silence %s until %s (%s), author %q",
		strings.Join(matchers, " "), end.Format("2006-01-02 15:04"), so.Duration, so.Author)
	if so.DryRun {
		s.Add(report.Info, "dry run: no silence created")
		return s
	}
	pod, err := alertmanagerPod(ctx, env, o)
	if err != nil {
		return s.Fail(err, "")
	}
	args := []string{"silence", "add",
		"--duration=" + so.Duration.String(),
		"--author=" + so.Author,
		"--comment=" + so.Comment,
	}
	args = append(args, matchers...)
	out, err := amtool(ctx, env, o, pod, args...)
	if err != nil {
		return s.Fail(err, "")
	}
	id := strings.TrimSpace(out)
	s.Add(report.Pass, "Silence created — UUID: %s", id)
	s.Add(report.Info, "To expire it: oc precheck silence expire %s", id)
	return s
}

// ActionExpireSilence expires existing silences.
func ActionExpireSilence(ctx context.Context, env *Env, o *Options, ids []string, dryRun bool) *report.Section {
	s := report.NewSection("expire-silence", "Action: expire Alertmanager silence(s)")
	s.Summary = strings.Join(ids, ", ")
	if dryRun {
		s.Add(report.Info, "dry run: nothing expired")
		return s
	}
	pod, err := alertmanagerPod(ctx, env, o)
	if err != nil {
		return s.Fail(err, "")
	}
	for _, id := range ids {
		if _, err := amtool(ctx, env, o, pod, "silence", "expire", id); err != nil {
			s.Add(report.Error, "%s: %v", id, err)
			continue
		}
		s.Add(report.Pass, "Silence %s expired", id)
	}
	return s
}
