// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/itlinuxfr/oc-upkit/internal/checks"
	"github.com/itlinuxfr/oc-upkit/internal/report"
)

func obj(apiVersion, kind, ns, name string, labels map[string]any, fields map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name}
	if ns != "" {
		meta["namespace"] = ns
	}
	if labels != nil {
		meta["labels"] = labels
	}
	o := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta}
	for k, v := range fields {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

// TestExampleScenarioRules runs the rules of examples/scenario-custom-rules.yaml
// (the ones of the guide) against a fake cluster.
func TestExampleScenarioRules(t *testing.T) {
	sc, _, _, err := Load("../../examples/scenario-custom-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range checks.All() {
		names = append(names, c.Name)
	}
	if err := sc.Validate(names); err != nil {
		t.Fatal(err)
	}
	rules, err := checks.CompileCustomRules(sc.CustomRules)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 {
		t.Fatalf("%d rules", len(rules))
	}

	recent := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	old := time.Now().Add(-5 * time.Hour).UTC().Format(time.RFC3339)
	objs := []runtime.Object{
		obj("argoproj.io/v1alpha1", "Application", "openshift-gitops", "ok", nil, map[string]any{"status": map[string]any{
			"sync": map[string]any{"status": "Synced"}, "health": map[string]any{"status": "Healthy"}}}),
		obj("argoproj.io/v1alpha1", "Application", "openshift-gitops", "drift", nil, map[string]any{"status": map[string]any{
			"sync": map[string]any{"status": "OutOfSync"}, "health": map[string]any{"status": "Healthy"}}}),
		obj("argoproj.io/v1alpha1", "Application", "openshift-gitops", "new", nil, nil),
		obj("apps/v1", "Deployment", "app", "web", nil, map[string]any{"spec": map[string]any{"replicas": int64(1)}}),
		obj("apps/v1", "Deployment", "app", "api", nil, map[string]any{"spec": map[string]any{"replicas": int64(3)}}),
		obj("apps/v1", "Deployment", "app", "stopped", nil, map[string]any{"spec": map[string]any{"replicas": int64(0)}}),
		obj("apps/v1", "Deployment", "openshift-console", "console", nil, map[string]any{"spec": map[string]any{"replicas": int64(1)}}),
		obj("v1", "Node", "", "worker-1", map[string]any{"example.com/maintenance": "true"}, nil),
		obj("v1", "Node", "", "worker-2", nil, nil),
		obj("batch/v1", "Job", "openshift-etcd", "etcd-backup-29001", nil, map[string]any{"status": map[string]any{
			"succeeded": int64(1), "completionTime": old}}),
		obj("batch/v1", "Job", "openshift-etcd", "etcd-backup-upkit-20261021", nil, map[string]any{"status": map[string]any{
			"failed": int64(1)}}),
		obj("batch/v1", "Job", "openshift-etcd", "other-recent", nil, map[string]any{"status": map[string]any{
			"succeeded": int64(1), "completionTime": recent}}),
	}
	kinds := map[schema.GroupVersionResource]string{
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:               "DeploymentList",
		{Version: "v1", Resource: "nodes"}:                                    "NodeList",
		{Group: "batch", Version: "v1", Resource: "jobs"}:                     "JobList",
	}
	run := func(objs ...runtime.Object) *report.Section {
		o := checks.DefaultOptions()
		// Same path as "oc upkit run": the rules go through Config.Apply.
		if err := (&checks.Config{CustomRules: sc.CustomRules}).Apply(o, func(string) bool { return false }); err != nil {
			t.Fatal(err)
		}
		env := &checks.Env{Dyn: dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objs...)}
		return checks.CheckCustomRules(context.Background(), env, o)
	}
	want := map[string]string{
		"site.argocd-synced":          "2 violation(s) out of 3",
		"site.min-replicas":           "1 violation(s) out of 3",
		"site.no-node-in-maintenance": "1 violation(s) out of 1",
		"site.etcd-backup-recent":     "1 violation(s) out of 2", // old success, recent failure
	}
	s := run(objs...)
	if s.Status != report.Crit {
		t.Errorf("status %v, want CRIT", s.Status)
	}
	for id, w := range want {
		found := false
		for _, f := range s.Findings {
			if strings.HasPrefix(f.Message, id+" ") {
				found = true
				if !strings.Contains(f.Message, w) {
					t.Errorf("%s: %q, want %q", id, f.Message, w)
				}
			}
			if f.Level == report.Error {
				t.Errorf("evaluation error: %s", f.Message)
			}
		}
		if !found {
			t.Errorf("%s: no finding", id)
		}
	}
	msgs := strings.Join(s.Tables[0].Rows[0], " ")
	for _, r := range s.Tables[0].Rows {
		msgs += " | " + strings.Join(r, " ")
	}
	for _, w := range []string{"sync=OutOfSync, health=Healthy", "no status yet", "only 1 replica(s)", "nodes in maintenance: worker-1", "No successful etcd backup"} {
		if !strings.Contains(msgs, w) {
			t.Errorf("message %q missing in %s", w, msgs)
		}
	}

	// A recent successful backup satisfies the backup rule.
	fresh := obj("batch/v1", "Job", "openshift-etcd", "etcd-backup-upkit-20261021b", nil, map[string]any{"status": map[string]any{
		"succeeded": int64(1), "completionTime": recent}})
	s = run(append(objs, fresh)...)
	for _, f := range s.Findings {
		if strings.HasPrefix(f.Message, "site.etcd-backup-recent ") && !strings.Contains(f.Message, "0 violation(s) out of 3") {
			t.Errorf("fresh backup: %q", f.Message)
		}
	}
}
