// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/itlinuxfr/oc-precheck/internal/report"
)

var badWaitingReasons = map[string]bool{
	"CrashLoopBackOff": true, "ImagePullBackOff": true, "ErrImagePull": true,
	"CreateContainerConfigError": true, "CreateContainerError": true, "InvalidImageName": true,
	"RunContainerError": true, "ErrImageNeverPull": true, "PreStartHookError": true, "PostStartHookError": true,
}

// PodStatus mirrors the READY / STATUS / RESTARTS columns of `oc get pods`.
type PodStatus struct {
	Ready, Total    int
	Status          string
	Restarts        int32
	BadWaiting      string // container waiting in an error state, if any
	LastTermination string
}

// GetPodStatus computes a kubectl-like status string.
func GetPodStatus(p corev1.Pod) PodStatus {
	st := PodStatus{Total: len(p.Spec.Containers)}
	reason := string(p.Status.Phase)
	if p.Status.Reason != "" {
		reason = p.Status.Reason
	}
	initializing := false
	for i, cs := range p.Status.InitContainerStatuses {
		st.Restarts += cs.RestartCount
		switch {
		case cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0:
			continue
		case cs.State.Terminated != nil:
			r := cs.State.Terminated.Reason
			if r == "" {
				r = fmt.Sprintf("ExitCode:%d", cs.State.Terminated.ExitCode)
			}
			reason = "Init:" + r
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + cs.State.Waiting.Reason
			if badWaitingReasons[cs.State.Waiting.Reason] {
				st.BadWaiting = cs.State.Waiting.Reason + " (init " + cs.Name + ")"
			}
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers))
		}
		initializing = true
		break
	}
	if !initializing {
		hasRunning := false
		for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
			cs := p.Status.ContainerStatuses[i]
			st.Restarts += cs.RestartCount
			if cs.Ready {
				st.Ready++
			}
			if t := cs.LastTerminationState.Terminated; t != nil && cs.RestartCount > 0 && t.Reason != "" {
				st.LastTermination = t.Reason
			}
			switch {
			case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
				reason = cs.State.Waiting.Reason
				if badWaitingReasons[reason] && st.BadWaiting == "" {
					st.BadWaiting = reason + " (" + cs.Name + ")"
				}
			case cs.State.Terminated != nil && cs.State.Terminated.Reason != "":
				reason = cs.State.Terminated.Reason
			case cs.State.Terminated != nil:
				reason = fmt.Sprintf("ExitCode:%d", cs.State.Terminated.ExitCode)
			case cs.Ready && cs.State.Running != nil:
				hasRunning = true
			}
		}
		if reason == "Completed" && hasRunning {
			reason = "Running"
		}
	} else {
		for _, cs := range p.Status.ContainerStatuses {
			st.Restarts += cs.RestartCount
			if cs.Ready {
				st.Ready++
			}
		}
	}
	if p.DeletionTimestamp != nil {
		if p.Status.Reason == "NodeLost" {
			reason = "Unknown"
		} else {
			reason = "Terminating"
		}
	}
	st.Status = reason
	return st
}

func platformNS(ns string) bool {
	return strings.HasPrefix(ns, "openshift-") || strings.HasPrefix(ns, "kube-") || ns == "default"
}

// CheckPods lists unhealthy pods: Running but not ready (0/1), waiting in
// error, Pending, stuck Terminating, Unknown and Failed.
func CheckPods(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("pods", "Unhealthy pods")
	pods, err := listPods(ctx, env, "", false, "")
	if err != nil {
		return s.Fail(err, "")
	}
	r := newRuler(s, o)
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})

	type hit struct {
		rule   string
		pod    corev1.Pod
		st     PodStatus
		detail string
	}
	var hits []hit
	now := time.Now()
	var active int

	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded {
			continue
		}
		active++
		st := GetPodStatus(p)
		rule, detail := "", ""
		switch {
		case p.DeletionTimestamp != nil:
			if now.Sub(p.DeletionTimestamp.Time) > o.PodTerminatingAge {
				rule, detail = "pods.terminating", "Terminating for "+age(p.DeletionTimestamp.Time)
				if len(p.Finalizers) > 0 {
					detail += " — finalizers: " + strings.Join(p.Finalizers, ",")
				}
			}
		case p.Status.Phase == corev1.PodFailed:
			rule, detail = "pods.failed", firstNonEmpty(p.Status.Reason, "Failed")
			if p.Status.Message != "" {
				detail += " — " + oneLine(p.Status.Message, 80)
			}
		case p.Status.Phase == corev1.PodUnknown:
			rule, detail = "pods.unknown", "unknown state (node unreachable?)"
		case st.BadWaiting != "":
			rule, detail = "pods.waiting-error", st.BadWaiting
			if st.LastTermination != "" {
				detail += " — last termination: " + st.LastTermination
			}
		case p.Status.Phase == corev1.PodPending:
			if now.Sub(p.CreationTimestamp.Time) > o.PodPendingAge {
				rule, detail = "pods.pending", "Pending for "+age(p.CreationTimestamp.Time)
				for _, c := range p.Status.Conditions {
					if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
						detail += " — " + oneLine(c.Message, 90)
					}
				}
			}
		case p.Status.Phase == corev1.PodRunning && st.Ready < st.Total:
			var readySince time.Time
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady {
					readySince = c.LastTransitionTime.Time
				}
			}
			if o.PodNotReadyAge == 0 || readySince.IsZero() || now.Sub(readySince) > o.PodNotReadyAge {
				rule = "pods.not-ready"
				detail = fmt.Sprintf("%d/%d container(s) ready for %s", st.Ready, st.Total, age(readySince))
				if st.LastTermination != "" {
					detail += " — last termination: " + st.LastTermination
				}
			}
		}
		if rule == "" && o.PodRestartWarn > 0 && st.Restarts >= o.PodRestartWarn {
			rule, detail = "pods.restarts", fmt.Sprintf("%d restarts", st.Restarts)
			if st.LastTermination != "" {
				detail += " — last termination: " + st.LastTermination
			}
		}
		if rule != "" {
			hits = append(hits, hit{rule, p, st, detail})
		}
	}

	// Most severe first, then namespace/name.
	sort.SliceStable(hits, func(i, j int) bool {
		li, _ := o.Policy.Effective(hits[i].rule)
		lj, _ := o.Policy.Effective(hits[j].rule)
		return li > lj
	})
	t := s.NewTable("", "Namespace", "Pod", "Ready", "Status", "Restarts", "Node", "Age", "Detail")
	counts := map[string]int{}
	var platform, user int
	for _, h := range hits {
		lvl, kept := r.eval(h.rule, h.pod.Namespace, h.pod.Name)
		if !kept {
			continue
		}
		counts[h.rule]++
		if lvl == report.Info && !o.Verbose {
			continue // failed pods / restarts: counted, listed with --verbose
		}
		if lvl != report.Info {
			if platformNS(h.pod.Namespace) {
				platform++
			} else {
				user++
			}
		}
		t.AddRow(lvl, h.pod.Namespace, h.pod.Name, fmt.Sprintf("%d/%d", h.st.Ready, h.st.Total), h.st.Status,
			fmt.Sprint(h.st.Restarts), orDash(h.pod.Spec.NodeName), age(h.pod.CreationTimestamp.Time), h.detail)
		s.Escalate(lvl)
	}
	t.Limit(o.MaxRows)

	labels := map[string]string{
		"pods.waiting-error": "in error", "pods.not-ready": "not ready", "pods.pending": "Pending",
		"pods.terminating": "stuck Terminating", "pods.unknown": "Unknown", "pods.failed": "Failed",
		"pods.restarts": "with high restarts",
	}
	var parts []string
	for _, k := range []string{"pods.waiting-error", "pods.not-ready", "pods.pending", "pods.terminating", "pods.unknown", "pods.failed", "pods.restarts"} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], labels[k]))
		}
	}
	s.Summary = fmt.Sprintf("%d pod(s) analysed", active)
	if len(parts) > 0 {
		s.Summary += " — " + strings.Join(parts, ", ")
	}
	if platform > 0 {
		s.Add(report.Info, "%d unhealthy pod(s) in platform namespaces (openshift-*, kube-*), %d in application namespaces", platform, user)
	}
	if (counts["pods.failed"] > 0 || counts["pods.restarts"] > 0) && !o.Verbose {
		s.Add(report.Info, "Failed pods and pods with high restarts are counted but not listed (use --verbose)")
	}
	r.done()
	return s
}
