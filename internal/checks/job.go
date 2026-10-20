// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// JobOptions configures the "run a Job from a CronJob" action.
type JobOptions struct {
	CronJob      string        // namespace/name
	Timeout      time.Duration // how long to wait for the Job to finish
	PollInterval time.Duration // 0 = 5s
	DryRun       bool
	Log          io.Writer // live progress
	Now          func() time.Time
}

// ActionJobFromCronJob creates a Job from the template of a CronJob, like
// `oc create job --from=cronjob/NAME`, and waits for it to succeed. Typical
// use: an etcd backup started by hand before an upgrade.
func ActionJobFromCronJob(ctx context.Context, env *Env, jo JobOptions) *report.Section {
	s := report.NewSection("job", "Action: run a Job from CronJob "+jo.CronJob)
	ns, name, ok := strings.Cut(jo.CronJob, "/")
	if !ok || ns == "" || name == "" {
		return s.Fail(fmt.Errorf("cronJob must be namespace/name, got %q", jo.CronJob), "")
	}
	now := time.Now
	if jo.Now != nil {
		now = jo.Now
	}
	logf := func(format string, a ...any) {
		if jo.Log != nil {
			fmt.Fprintf(jo.Log, "  [job] "+format+"\n", a...)
		}
	}

	cj, err := env.CS.BatchV1().CronJobs(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return s.Fail(err, "")
	}
	jobName := jobNameFor(name, now())
	if jo.DryRun {
		s.Summary = fmt.Sprintf("Job %s/%s would be created from the CronJob", ns, jobName)
		s.Add(report.Info, "dry run: no Job created")
		return s
	}

	ctrl := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        jobName,
			Namespace:   ns,
			Labels:      map[string]string{managedByLabel: managedByValue},
			Annotations: map[string]string{"cronjob.kubernetes.io/instantiate": "manual"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID, Controller: &ctrl,
			}},
		},
		Spec: cj.Spec.JobTemplate.Spec,
	}
	for k, v := range cj.Spec.JobTemplate.Labels {
		job.Labels[k] = v
	}
	if _, err := env.CS.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return s.Fail(err, "")
	}
	start := now()
	s.Add(report.Info, "Job %s/%s created", ns, jobName)
	logf("Job %s/%s created, waiting up to %s", ns, jobName, jo.Timeout)

	poll := jo.PollInterval
	if poll <= 0 {
		poll = 5 * time.Second
	}
	wctx, cancel := context.WithTimeout(ctx, jo.Timeout)
	defer cancel()
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		j, err := env.CS.BatchV1().Jobs(ns).Get(wctx, jobName, metav1.GetOptions{})
		switch {
		case err != nil && !apierrors.IsNotFound(err) && wctx.Err() == nil:
			logf("get Job: %v", err)
		case err == nil:
			if done, failed, msg := jobFinished(j); done {
				d := now().Sub(start).Round(time.Second)
				if failed {
					s.Summary = fmt.Sprintf("Job %s/%s failed after %s", ns, jobName, d)
					s.Add(report.Error, "Job failed: %s — see: oc -n %s logs job/%s", msg, ns, jobName)
					return s
				}
				s.Summary = fmt.Sprintf("Job %s/%s succeeded in %s", ns, jobName, d)
				s.Add(report.Pass, "Job succeeded")
				return s
			}
		}
		select {
		case <-wctx.Done():
			s.Summary = fmt.Sprintf("Job %s/%s still running after %s", ns, jobName, jo.Timeout)
			s.Add(report.Error, "Timeout waiting for the Job — check it with: oc -n %s get job %s", ns, jobName)
			return s
		case <-t.C:
		}
	}
}

// jobNameFor builds a Job name that fits the 63-character limit of the
// job-name label: <cronjob>-upkit-<YYYYMMDDHHMMSS>.
func jobNameFor(cronJob string, t time.Time) string {
	suffix := "-upkit-" + t.UTC().Format("20060102150405")
	if max := 63 - len(suffix); len(cronJob) > max {
		cronJob = strings.TrimRight(cronJob[:max], "-.")
	}
	return cronJob + suffix
}

// jobFinished reports whether a Job reached Complete or Failed.
func jobFinished(j *batchv1.Job) (done, failed bool, msg string) {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete, batchv1.JobSuccessCriteriaMet:
			return true, false, ""
		case batchv1.JobFailed, batchv1.JobFailureTarget:
			return true, true, firstNonEmpty(c.Message, c.Reason, "failed")
		}
	}
	return false, false, ""
}
