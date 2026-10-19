package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/itlinuxfr/oc-precheck/internal/report"
)

func boolPtr(b bool) *bool { return &b }

// ----------------------------------------------------------- policy

func TestPolicyOverridesAndExclusions(t *testing.T) {
	p := &Policy{
		Severities: map[string]string{"mcp.paused": "info", "pdb.*": "crit", "pdb.empty": "off"},
		Exclusions: []Exclusion{{Rule: "pods.*", Namespaces: []string{"openshift-*"}}, {Rule: "csr.pending", Names: []string{"csr-ok-*"}}},
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		rule string
		lvl  report.Level
		on   bool
	}{
		{"mcp.paused", report.Info, true},
		{"pdb.blocking", report.Crit, true},
		{"pdb.empty", report.Pass, false},
		{"nodes.not-ready", report.Crit, true},
	}
	for _, c := range cases {
		l, on := p.Effective(c.rule)
		if l != c.lvl || on != c.on {
			t.Errorf("%s: got %v/%v want %v/%v", c.rule, l, on, c.lvl, c.on)
		}
	}
	if !p.Excluded("pods.not-ready", "openshift-dns", "x") || p.Excluded("pods.not-ready", "app", "x") {
		t.Error("namespace exclusion")
	}
	if !p.Excluded("csr.pending", "", "csr-ok-1") || p.Excluded("csr.pending", "", "csr-other") {
		t.Error("name exclusion")
	}
	bad := &Policy{Severities: map[string]string{"nope.rule": "warn"}}
	if bad.Validate() == nil {
		t.Error("unknown rule should fail validation")
	}
}

// ----------------------------------------------------- config + CEL

const testConfig = `
settings:
  quotaThreshold: 80
  podPendingAge: 2m
  etcdDBSizeWarn: 4Gi
  groupByLabels: [example.com/room]
severities:
  mcp.paused: info
exclusions:
  - rule: "pods.*"
    namespaces: ["sandbox-*"]
customRules:
  - id: org.deploy-replicas
    description: "Application Deployments need at least 2 replicas"
    apiVersion: apps/v1
    resource: deployments
    match: '!object.metadata.namespace.startsWith("openshift-")'
    expr: 'has(object.spec.replicas) && object.spec.replicas >= params.minReplicas'
    messageExpression: '"replicas=" + string(object.spec.replicas) + " < " + string(params.minReplicas)'
    params:
      minReplicas: 2
  - id: org.max-deployments
    apiVersion: apps/v1
    resource: deployments
    aggregate: true
    severity: crit
    expr: 'size(objects) <= 2'
    message: "too many deployments"
`

func writeTemp(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "precheck.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigLoadAndCustomRules(t *testing.T) {
	cfg, err := LoadConfig(writeTemp(t, testConfig))
	if err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	changed := func(f string) bool { return f == "quota-threshold" } // flag given explicitly
	if err := cfg.Apply(o, changed); err != nil {
		t.Fatal(err)
	}
	if o.QuotaThreshold != 90 {
		t.Errorf("explicit flag must win: quotaThreshold=%v", o.QuotaThreshold)
	}
	if o.PodPendingAge != 2*time.Minute || o.EtcdDBSizeWarn != 4<<30 || o.GroupByLabels[0] != "example.com/room" {
		t.Errorf("settings not applied: %v %v %v", o.PodPendingAge, o.EtcdDBSizeWarn, o.GroupByLabels)
	}
	if len(o.CustomRules) != 2 {
		t.Fatalf("custom rules = %d", len(o.CustomRules))
	}

	dep := func(ns, name string, replicas int64) runtime.Object {
		return u("apps/v1", "Deployment", ns, name, map[string]any{"spec": map[string]any{"replicas": replicas}})
	}
	env := &Env{Dyn: newDyn(dep("app", "web", 1), dep("app", "api", 3), dep("openshift-x", "op", 1))}
	s := CheckCustomRules(context.Background(), env, o)
	if s.Status != report.Crit {
		t.Fatalf("status %v findings %+v", s.Status, s.Findings)
	}
	rows := s.Tables[0].Rows
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0][0] != "org.deploy-replicas" || rows[0][2] != "web" || rows[0][3] != "replicas=1 < 2" {
		t.Fatalf("row = %v", rows[0])
	}
	if rows[1][0] != "org.max-deployments" || rows[1][3] != "too many deployments" {
		t.Fatalf("aggregate row = %v", rows[1])
	}
}

func TestConfigRejectsTyposAndBadCEL(t *testing.T) {
	if _, err := LoadConfig(writeTemp(t, "setings:\n  maxRows: 3\n")); err == nil {
		t.Error("unknown key must fail")
	}
	cfg, err := LoadConfig(writeTemp(t, `
customRules:
  - id: bad
    apiVersion: v1
    resource: pods
    expr: 'object.spec.containers.size( >'
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(DefaultOptions(), func(string) bool { return false }); err == nil || !strings.Contains(err.Error(), "expr") {
		t.Errorf("bad CEL must fail at load, got %v", err)
	}
}

// -------------------------------------------------------------- pods

func TestPodsStatuses(t *testing.T) {
	now := metav1.Now()
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	running := func(name, ns string, ready bool) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: old},
			Spec:       corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "c"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning,
				Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: map[bool]corev1.ConditionStatus{true: "True", false: "False"}[ready], LastTransitionTime: old}},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "c", Ready: ready, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
		}
	}
	crash := running("crash", "app", false)
	crash.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	crash.Status.ContainerStatuses[0].RestartCount = 12
	crash.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}
	evicted := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "evicted", Namespace: "app"},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}}
	term := running("stuck", "app", true)
	term.DeletionTimestamp = &old
	term.Finalizers = []string{"x/y"}
	fresh := running("fresh", "app", true)
	fresh.DeletionTimestamp = &now

	cs := fake.NewSimpleClientset(running("ok", "app", true), running("zero", "app", false), crash, evicted, term, fresh)
	s := CheckPods(context.Background(), &Env{CS: cs}, DefaultOptions())
	rows := s.Tables[0].Rows
	got := map[string][]string{}
	for _, r := range rows {
		got[r[1]] = r
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	if got["zero"][2] != "0/1" || got["zero"][3] != "Running" {
		t.Errorf("zero = %v", got["zero"])
	}
	if got["crash"][3] != "CrashLoopBackOff" || !strings.Contains(got["crash"][7], "OOMKilled") {
		t.Errorf("crash = %v", got["crash"])
	}
	if got["stuck"][3] != "Terminating" {
		t.Errorf("stuck = %v", got["stuck"])
	}
	if !strings.Contains(s.Summary, "1 Failed") {
		t.Errorf("summary = %q", s.Summary)
	}
}

// ------------------------------------------------------------ quotas

func TestQuotas(t *testing.T) {
	rq := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "q", Namespace: "app"},
		Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10"), corev1.ResourcePods: resource.MustParse("20")},
			Used: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("9500m"), corev1.ResourcePods: resource.MustParse("5")},
		},
	}
	crq := u("quota.openshift.io/v1", "ClusterResourceQuota", "", "team-a", map[string]any{
		"status": map[string]any{
			"total":      map[string]any{"hard": map[string]any{"limits.memory": "10Gi"}, "used": map[string]any{"limits.memory": "10Gi"}},
			"namespaces": []any{map[string]any{"namespace": "a"}, map[string]any{"namespace": "b"}},
		},
	})
	s := CheckQuotas(context.Background(), &Env{CS: fake.NewSimpleClientset(rq), Dyn: newDyn(crq)}, DefaultOptions())
	rows := s.Tables[0].Rows
	if len(rows) != 2 || rows[0][3] != "cpu" || rows[0][6] != "95%" || rows[1][2] != "team-a" || rows[1][6] != "100%" {
		t.Fatalf("rows = %v", rows)
	}
	if s.Status != report.Warn {
		t.Fatalf("status = %v", s.Status)
	}
}

// -------------------------------------------------------------- jobs

func TestJobs(t *testing.T) {
	start := metav1.NewTime(time.Now().Add(-3 * time.Hour))
	recent := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	mk := func(name string, st metav1.Time) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "batch",
			OwnerReferences: []metav1.OwnerReference{{Kind: "CronJob", Name: "nightly"}}},
			Status: batchv1.JobStatus{Active: 1, StartTime: &st}}
	}
	done := mk("done", start)
	ct := metav1.Now()
	done.Status.CompletionTime = &ct
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "long-x", Namespace: "batch", Labels: map[string]string{"batch.kubernetes.io/job-name": "long"}},
		Spec: corev1.PodSpec{NodeName: "w3"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	s := CheckJobs(context.Background(), &Env{CS: fake.NewSimpleClientset(mk("long", start), mk("short", recent), done, pod)}, DefaultOptions())
	rows := s.Tables[0].Rows
	if len(rows) != 2 || rows[0][1] != "long" || rows[0][2] != "nightly" || !strings.HasPrefix(rows[0][4], "3h") || rows[0][8] != "w3" {
		t.Fatalf("rows = %v", rows)
	}
	if s.Status != report.Warn || s.Tables[0].RowLevels[1] != report.Info {
		t.Fatalf("status %v levels %v", s.Status, s.Tables[0].RowLevels)
	}
}

// ------------------------------------------------------------- HCP

func TestHostedClusters(t *testing.T) {
	hc := func(name string, conds []any, desired string) runtime.Object {
		return u("hypershift.openshift.io/v1beta1", "HostedCluster", "clusters", name, map[string]any{
			"spec": map[string]any{},
			"status": map[string]any{
				"conditions": conds,
				"version": map[string]any{
					"desired": map[string]any{"version": desired},
					"history": []any{map[string]any{"version": "4.17.30", "state": "Completed"}},
				},
			},
		})
	}
	np := func(name, cluster string, desired, current int64, conds []any) runtime.Object {
		return u("hypershift.openshift.io/v1beta1", "NodePool", "clusters", name, map[string]any{
			"spec":   map[string]any{"clusterName": cluster, "replicas": desired, "management": map[string]any{"upgradeType": "Replace", "autoRepair": true}},
			"status": map[string]any{"replicas": current, "version": "4.17.30", "conditions": conds},
		})
	}
	okConds := []any{cond("Available", "True", ""), cond("Degraded", "False", ""), cond("Progressing", "False", ""),
		cond("EtcdAvailable", "True", ""), cond("KubeAPIServerAvailable", "True", "")}
	badConds := []any{cond("Available", "False", "kas down"), cond("Degraded", "True", "etcd KO"), cond("EtcdAvailable", "False", "")}
	npOK := []any{cond("Ready", "True", ""), cond("AllNodesHealthy", "True", ""), cond("UpdatingVersion", "False", ""), cond("UpdatingConfig", "False", "")}
	npUpd := []any{cond("Ready", "True", ""), cond("AllNodesHealthy", "True", ""), cond("UpdatingVersion", "True", ""), cond("UpdatingConfig", "False", "")}

	mc := u("cluster.open-cluster-management.io/v1", "ManagedCluster", "", "prod1", map[string]any{
		"status": map[string]any{"conditions": []any{cond("ManagedClusterConditionAvailable", "Unknown", "")}}})
	cpPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "etcd-0", Namespace: "clusters-prod1", Labels: map[string]string{"app": "etcd"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "etcd"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "etcd", Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}

	env := &Env{
		CS: fake.NewSimpleClientset(cpPod),
		Dyn: newDyn(hc("prod1", okConds, "4.17.30"), hc("prod2", badConds, "4.17.30"), hc("dev1", okConds, "4.18.24"),
			np("prod1-w", "prod1", 3, 3, npOK), np("prod2-w", "prod2", 3, 2, npOK), np("dev1-w", "dev1", 2, 2, npUpd), mc),
	}
	s := CheckHostedClusters(context.Background(), env, DefaultOptions())
	if s.Status != report.Crit {
		t.Fatalf("status %v", s.Status)
	}
	status := map[string]string{}
	for _, r := range s.Tables[0].Rows {
		status[r[0]] = r[len(r)-1]
	}
	if status["clusters/prod2"] != "CRIT" || status["clusters/dev1"] != "WARN" || status["clusters/prod1"] != "WARN" {
		t.Fatalf("global status = %v (prod1 WARN due to ACM Unknown)", status)
	}
	if !strings.Contains(s.Summary, "3 HostedCluster(s): 0 OK, 2 WARN, 1 CRIT") {
		t.Fatalf("summary %q", s.Summary)
	}
	if !hasFinding(s, report.Warn, "[clusters/prod1] ACM ManagedCluster") {
		t.Fatalf("ACM finding missing %+v", s.Findings)
	}

	o := DefaultOptions()
	o.HCPFilter = []string{"prod*"}
	if s := CheckHostedClusters(context.Background(), env, o); len(s.Tables[0].Rows) != 2 {
		t.Fatalf("filter: rows %v", s.Tables[0].Rows)
	}
	if s := CheckHostedClusters(context.Background(), &Env{CS: fake.NewSimpleClientset(), Dyn: dynWithout()}, DefaultOptions()); s.Status != report.Skip {
		t.Fatalf("non-hub must SKIP, got %v", s.Status)
	}
}

// dynWithout returns a dynamic client answering NotFound for HyperShift,
// emulating a cluster without the CRDs.
func dynWithout() *dynfake.FakeDynamicClient {
	d := newDyn()
	d.PrependReactor("list", "hostedclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(gvrHostedCluster.GroupResource(), "")
	})
	return d
}

// ------------------------------------------------------------ drain

func node(name, zone string, cp bool) *corev1.Node {
	l := map[string]string{"topology.kubernetes.io/zone": zone, "node-role.kubernetes.io/worker": ""}
	if cp {
		l = map[string]string{"node-role.kubernetes.io/master": "", "topology.kubernetes.io/zone": zone}
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("100"),
				corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("32Gi")}}}
}

func rsPod(name, ns, nodeName string, emptyDir bool) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name), Labels: map[string]string{"app": "web"},
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-1", Controller: boolPtr(true)}}},
		Spec:   corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "c"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if emptyDir {
		p.Spec.Volumes = []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	}
	return p
}

func TestDrainRoom(t *testing.T) {
	ds := rsPod("ds-a1", "openshift-dns", "a1", false)
	ds.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", Name: "dns", Controller: boolPtr(true)}}
	naked := rsPod("naked", "app", "a2", false)
	naked.OwnerReferences = nil
	min1 := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"},
		Spec:   policyv1.PodDisruptionBudgetSpec{MinAvailable: &min1, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}},
		Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 1}}
	cs := fake.NewSimpleClientset(node("a1", "room-a", false), node("a2", "room-a", false), node("b1", "room-b", false),
		node("m1", "room-a", true), rsPod("web-1", "app", "a1", false), rsPod("web-2", "app", "a2", true), ds, naked, pdb)
	// Evictions remove the pod from the fake tracker.
	evicted := map[string]bool{}
	cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		ev := a.(k8stesting.CreateAction).GetObject().(*policyv1.Eviction)
		evicted[ev.Name] = true
		return true, nil, cs.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), ev.Namespace, ev.Name)
	})
	env := &Env{CS: cs}
	o := DefaultOptions()
	sel := "topology.kubernetes.io/zone=room-a,node-role.kubernetes.io/worker"

	// Without flags: emptyDir + naked pod are blockers.
	plan := PlanDrain(context.Background(), env, o, DrainOptions{Selector: sel})
	if len(plan.Nodes) != 2 || plan.Blocking != 2 {
		t.Fatalf("nodes=%d blocking=%d", len(plan.Nodes), plan.Blocking)
	}
	// Control plane in selection is refused.
	if p := PlanDrain(context.Background(), env, o, DrainOptions{Selector: "topology.kubernetes.io/zone=room-a"}); p.Section.Status != report.Error {
		t.Fatalf("control plane must be refused, got %v", p.Section.Status)
	}

	do := DrainOptions{Selector: sel, DeleteEmptyDir: true, Force: true, Parallel: 2, PodParallel: 2, Timeout: 10 * time.Second, GracePeriod: -1}
	plan = PlanDrain(context.Background(), env, o, do)
	if plan.Blocking != 0 {
		t.Fatalf("blocking = %d", plan.Blocking)
	}
	s := ExecuteDrain(context.Background(), env, o, plan, do)
	if s.Status != report.Pass {
		t.Fatalf("drain status %v %+v", s.Status, s.Findings)
	}
	if !evicted["web-1"] || !evicted["web-2"] || evicted["ds-a1"] {
		t.Fatalf("evicted = %v", evicted)
	}
	if _, err := cs.CoreV1().Pods("app").Get(context.Background(), "naked", metav1.GetOptions{}); err == nil {
		t.Fatal("naked pod should have been deleted (--force)")
	}
	for _, n := range []string{"a1", "a2"} {
		nn, _ := cs.CoreV1().Nodes().Get(context.Background(), n, metav1.GetOptions{})
		if !nn.Spec.Unschedulable {
			t.Fatalf("%s not cordoned", n)
		}
	}
	if b1, _ := cs.CoreV1().Nodes().Get(context.Background(), "b1", metav1.GetOptions{}); b1.Spec.Unschedulable {
		t.Fatal("b1 must not be cordoned")
	}
	us := Uncordon(context.Background(), env, sel, false)
	if us.Status != report.Pass {
		t.Fatalf("uncordon %v", us.Status)
	}
	if a1, _ := cs.CoreV1().Nodes().Get(context.Background(), "a1", metav1.GetOptions{}); a1.Spec.Unschedulable {
		t.Fatal("a1 still cordoned")
	}
}

// ----------------------------------------------------------- banner

func TestBanner(t *testing.T) {
	env := &Env{Dyn: newDyn()}
	ctx := context.Background()
	s := ActionBanner(ctx, env, BannerOptions{Name: "maint", Text: "Upgrade in progress", Background: "#c9190b", LinkURL: "https://chg/1"})
	if s.Status != report.Pass {
		t.Fatalf("create %v %+v", s.Status, s.Findings)
	}
	s = ActionBanner(ctx, env, BannerOptions{Name: "maint", Text: "Upgrade completed", Location: "BannerTopBottom"})
	if !hasFinding(s, report.Pass, "updated") {
		t.Fatalf("update %+v", s.Findings)
	}
	l := ListBanners(ctx, env)
	if r := l.Tables[0].Rows; len(r) != 1 || r[0][2] != "Upgrade completed" || r[0][1] != "BannerTopBottom" || r[0][5] != "oc-precheck" {
		t.Fatalf("list %v", r)
	}
	if s := ActionBanner(ctx, env, BannerOptions{Name: "x", Text: "t", Location: "Middle"}); s.Status != report.Error {
		t.Fatal("bad location must fail")
	}
	ActionRemoveBanner(ctx, env, []string{"maint"}, false, false)
	if l := ListBanners(ctx, env); len(l.Tables[0].Rows) != 0 {
		t.Fatal("banner not removed")
	}
}

// ---------------------------------------------------------- silences

func TestSilences(t *testing.T) {
	am := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "alertmanager-main-0", Namespace: "openshift-monitoring",
		Labels: map[string]string{"app.kubernetes.io/name": "alertmanager"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	fe := &fakeExec{handler: func(c execCall) (string, error) {
		if strings.Contains(strings.Join(c.cmd, " "), "silence query") {
			return fmt.Sprintf(`[{"id":"6f0c3c2e-1111-2222-3333-444455556666","createdBy":"ben","comment":"upgrade","startsAt":"2026-10-19T20:00:00Z","endsAt":%q,
  "status":{"state":"active"},"matchers":[{"name":"alertname","value":".+","isRegex":true,"isEqual":true}]},
 {"id":"aaaa","createdBy":"alexis","comment":"x","startsAt":"2026-10-19T20:00:00Z","endsAt":%q,
  "status":{"state":"active"},"matchers":[{"name":"namespace","value":"app","isRegex":false}]}]`, end, end), nil
		}
		return "", fmt.Errorf("unexpected")
	}}
	s := CheckSilences(context.Background(), &Env{CS: fake.NewSimpleClientset(am), Exec: fe}, DefaultOptions())
	rows := s.Tables[0].Rows
	if len(rows) != 2 || rows[0][0] != "6f0c3c2e-1111-2222-3333-444455556666" || rows[0][6] != "alertname=~.+" || rows[1][6] != "namespace=app" {
		t.Fatalf("rows %v", rows)
	}
	if s.Status != report.Warn || !hasFinding(s, report.Warn, "mutes ALL") {
		t.Fatalf("match-all must warn: %v %+v", s.Status, s.Findings)
	}
}

// ------------------------------------------------------------- watch

func TestWatchSnapshot(t *testing.T) {
	n1 := node("w1", "room-a", false)
	n1.Annotations = map[string]string{annMCState: "Working", annMCCurrent: "rendered-worker-aaaaaaaaaaaa", annMCDesired: "rendered-worker-bbbbbbbbbbbb"}
	n1.Spec.Unschedulable = true
	n2 := node("w2", "room-b", false)
	n2.Annotations = map[string]string{annMCState: "Done", annMCCurrent: "rendered-worker-bbbbbbbbbbbb", annMCDesired: "rendered-worker-bbbbbbbbbbbb"}
	env := &Env{CS: fake.NewSimpleClientset(n1, n2), Dyn: newDyn()}
	secs := WatchSnapshot(context.Background(), env, DefaultOptions(), WatchOptions{Pods: true})
	var ns *report.Section
	for _, s := range secs {
		if s.Name == "nodes" {
			ns = s
		}
	}
	if ns == nil {
		t.Fatal("nodes section missing")
	}
	rows := ns.Tables[0].Rows
	if rows[0][7] != "aaaaaaaa → bbbbbbbb" || rows[0][3] != "Ready,SchedDisabled" || rows[1][7] != "✓ bbbbbbbb" {
		t.Fatalf("rows %v", rows)
	}
	if !strings.Contains(ns.Summary, "config up to date 1/2") {
		t.Fatalf("summary %q", ns.Summary)
	}
	secs = WatchSnapshot(context.Background(), env, DefaultOptions(), WatchOptions{Problems: true})
	for _, s := range secs {
		if s.Name == "nodes" && len(s.Tables[0].Rows) != 1 {
			t.Fatalf("problems filter: %v", s.Tables[0].Rows)
		}
	}
}

// ------------------------------------------- every catalog rule is reachable

func TestCatalogUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Catalog {
		if seen[r.ID] {
			t.Errorf("duplicate rule %s", r.ID)
		}
		seen[r.ID] = true
		found := false
		for _, c := range All() {
			if c.Name == r.Check {
				found = true
			}
		}
		if !found {
			t.Errorf("rule %s references unknown check %s", r.ID, r.Check)
		}
	}
}

// The shipped example configuration must load and its rules must behave.
func TestExampleConfig(t *testing.T) {
	cfg, err := LoadConfig("../../examples/precheck.yaml")
	if err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	if err := cfg.Apply(o, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if l, on := o.Policy.Effective("pods.failed"); on {
		t.Fatalf("pods.failed should be off, got %v", l)
	}
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().UTC().Format(time.RFC3339)
	pvc := func(name, ts, phase string) runtime.Object {
		o := u("v1", "PersistentVolumeClaim", "app", name, map[string]any{"status": map[string]any{"phase": phase}})
		o.Object["metadata"].(map[string]any)["creationTimestamp"] = ts
		return o
	}
	maint := u("v1", "Node", "", "w9", nil)
	maint.SetLabels(map[string]string{"example.com/maintenance": "true"})
	normal := u("v1", "Node", "", "w1", nil)
	sub := u("operators.coreos.com/v1alpha1", "Subscription", "openshift-operators", "odf", map[string]any{
		"spec": map[string]any{"name": "odf-operator", "installPlanApproval": "Automatic"}})
	dep := u("apps/v1", "Deployment", "app", "front", map[string]any{"spec": map[string]any{"replicas": int64(1)}})
	env := &Env{Dyn: newDyn(pvc("old", old, "Pending"), pvc("new", recent, "Pending"), pvc("bound", old, "Bound"),
		maint, normal, sub, dep)}
	s := CheckCustomRules(context.Background(), env, o)
	got := map[string]string{}
	for _, r := range s.Tables[0].Rows {
		got[r[0]] = r[2] + " | " + r[3]
	}
	want := map[string]string{
		"org.olm-manual-approval":    "odf | Subscription odf-operator uses Automatic approval",
		"org.deploy-min-replicas":    "front | single replica: front will be unavailable during the drain",
		"org.pvc-pending":            "old | PVC Pending for more than 10 minutes",
		"org.no-node-in-maintenance": "(1 objects) | nodes in maintenance: w9",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected violations: %v", got)
	}
	for _, f := range s.Findings {
		if f.Level == report.Error {
			t.Errorf("evaluation error: %s", f.Message)
		}
	}
}

// The generated default configuration must load and reproduce the defaults.
func TestDefaultConfigRoundTrip(t *testing.T) {
	cfg, err := ParseConfig([]byte(DefaultConfigYAML()))
	if err != nil {
		t.Fatalf("default config does not parse: %v", err)
	}
	o := DefaultOptions()
	if err := cfg.Apply(o, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	d := DefaultOptions()
	if o.MaxRows != d.MaxRows || o.EtcdFragThreshold != d.EtcdFragThreshold || o.EtcdDBSizeWarn != d.EtcdDBSizeWarn ||
		o.PodPendingAge != d.PodPendingAge || o.PodNotReadyAge != d.PodNotReadyAge || o.PodTerminatingAge != d.PodTerminatingAge ||
		o.JobLongDuration != d.JobLongDuration || o.QuotaThreshold != d.QuotaThreshold || o.ZoneLabel != d.ZoneLabel ||
		strings.Join(o.IgnoreAlerts, ",") != strings.Join(d.IgnoreAlerts, ",") {
		t.Fatalf("settings differ from defaults:\n%+v\n%+v", o, d)
	}
	if len(cfg.Severities) != len(Catalog) {
		t.Fatalf("severities: %d entries, catalog has %d", len(cfg.Severities), len(Catalog))
	}
	for _, r := range Catalog {
		if l, on := o.Policy.Effective(r.ID); !on || l != r.Level {
			t.Errorf("%s: effective %v/%v, default %v", r.ID, l, on, r.Level)
		}
	}
}
