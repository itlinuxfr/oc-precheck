// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

func backupCronJob() *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "etcd-backup", Namespace: "openshift-etcd", UID: "uid-1"},
		Spec: batchv1.CronJobSpec{Schedule: "0 3 * * *", JobTemplate: batchv1.JobTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "backup"}},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers:    []corev1.Container{{Name: "backup", Image: "backup:1"}},
			}}},
		}},
	}
}

// finishJobs makes every Job read back as finished with the given condition.
func finishJobs(cs *fake.Clientset, cond batchv1.JobConditionType, msg string) {
	cs.PrependReactor("get", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		name := a.(k8stesting.GetAction).GetName()
		obj, err := cs.Tracker().Get(batchv1.SchemeGroupVersion.WithResource("jobs"), a.GetNamespace(), name)
		if err != nil {
			return true, nil, err
		}
		j := obj.(*batchv1.Job).DeepCopy()
		j.Status.Conditions = []batchv1.JobCondition{{Type: cond, Status: corev1.ConditionTrue, Message: msg}}
		return true, j, nil
	})
}

func TestJobFromCronJob(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 20, 21, 30, 0, 0, time.UTC)
	jo := JobOptions{CronJob: "openshift-etcd/etcd-backup", Timeout: time.Second, PollInterval: time.Millisecond, Now: func() time.Time { return at }}

	cs := fake.NewSimpleClientset(backupCronJob())
	finishJobs(cs, batchv1.JobComplete, "")
	s := ActionJobFromCronJob(ctx, &Env{CS: cs}, jo)
	if s.Status != report.Pass || !strings.Contains(s.Summary, "succeeded") {
		t.Fatalf("success: %v %q %+v", s.Status, s.Summary, s.Findings)
	}
	j, err := cs.BatchV1().Jobs("openshift-etcd").Get(ctx, "etcd-backup-upkit-20261020213000", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if j.Labels["app"] != "backup" || j.Labels[managedByLabel] != managedByValue ||
		j.Annotations["cronjob.kubernetes.io/instantiate"] != "manual" || j.OwnerReferences[0].UID != "uid-1" ||
		j.Spec.Template.Spec.Containers[0].Image != "backup:1" {
		t.Fatalf("job: %+v", j)
	}

	cs = fake.NewSimpleClientset(backupCronJob())
	finishJobs(cs, batchv1.JobFailed, "BackoffLimitExceeded")
	if s := ActionJobFromCronJob(ctx, &Env{CS: cs}, jo); s.Status != report.Error || !hasFinding(s, report.Error, "BackoffLimitExceeded") {
		t.Fatalf("failure: %v %+v", s.Status, s.Findings)
	}

	// Never finishes: timeout.
	cs = fake.NewSimpleClientset(backupCronJob())
	jo2 := jo
	jo2.Timeout = 20 * time.Millisecond
	if s := ActionJobFromCronJob(ctx, &Env{CS: cs}, jo2); s.Status != report.Error || !hasFinding(s, report.Error, "Timeout") {
		t.Fatalf("timeout: %v %+v", s.Status, s.Findings)
	}

	// Dry run creates nothing; a missing CronJob is an error.
	cs = fake.NewSimpleClientset(backupCronJob())
	jo3 := jo
	jo3.DryRun = true
	if s := ActionJobFromCronJob(ctx, &Env{CS: cs}, jo3); s.Status != report.Pass {
		t.Fatalf("dry run: %+v", s)
	}
	if l, _ := cs.BatchV1().Jobs("openshift-etcd").List(ctx, metav1.ListOptions{}); len(l.Items) != 0 {
		t.Fatal("dry run created a Job")
	}
	jo3.CronJob = "openshift-etcd/missing"
	if s := ActionJobFromCronJob(ctx, &Env{CS: cs}, jo3); s.Status != report.Error {
		t.Fatalf("missing CronJob: %+v", s)
	}
}

func TestJobNameFor(t *testing.T) {
	at := time.Date(2026, 10, 20, 21, 30, 0, 0, time.UTC)
	long := strings.Repeat("a", 70)
	if n := jobNameFor(long, at); len(n) > 63 || !strings.HasSuffix(n, "-upkit-20261020213000") {
		t.Fatalf("%q (%d)", n, len(n))
	}
}

func TestSilenceCreatedByTool(t *testing.T) {
	cases := []struct {
		x    Silence
		want bool
	}{
		{Silence{CreatedBy: "ben (oc-upkit)"}, true},
		{Silence{CreatedBy: "alexis", Comment: "OpenShift upgrade (oc-upkit)"}, true},
		{Silence{CreatedBy: "ben (oc-precheck)"}, true},
		{Silence{CreatedBy: "alexis", Comment: "manual"}, false},
	}
	for _, c := range cases {
		if c.x.CreatedByTool() != c.want {
			t.Errorf("%+v: want %v", c.x, c.want)
		}
	}
}
