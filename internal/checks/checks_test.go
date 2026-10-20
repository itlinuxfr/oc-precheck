package checks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// ------------------------------------------------------------ fakes

type execCall struct {
	ns, pod, container string
	cmd                []string
}

type fakeExec struct {
	mu      sync.Mutex
	calls   []execCall
	handler func(c execCall) (string, error)
}

func (f *fakeExec) Exec(_ context.Context, ns, pod, container string, cmd []string) (string, string, error) {
	c := execCall{ns, pod, container, cmd}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	out, err := f.handler(c)
	return out, "", err
}

func newDyn(objs ...runtime.Object) *dynfake.FakeDynamicClient {
	kinds := map[schema.GroupVersionResource]string{
		gvrClusterVersion: "ClusterVersionList", gvrClusterOperator: "ClusterOperatorList",
		gvrInfrastructure: "InfrastructureList", gvrEtcdOperator: "EtcdList", gvrMCP: "MachineConfigPoolList",
		gvrCSV: "ClusterServiceVersionList", gvrSubscription: "SubscriptionList", gvrCatalogSource: "CatalogSourceList",
		gvrHostedCluster: "HostedClusterList", gvrNodePool: "NodePoolList", gvrManagedCluster: "ManagedClusterList",
		gvrClusterQuota: "ClusterResourceQuotaList", gvrConsoleNotif: "ConsoleNotificationList",
		{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
		{Version: "v1", Resource: "persistentvolumeclaims"}:     "PersistentVolumeClaimList",
		{Version: "v1", Resource: "nodes"}:                      "NodeList",
	}
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objs...)
}

func u(apiVersion, kind, ns, name string, fields map[string]any) *unstructured.Unstructured {
	obj := map[string]any{"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns}}
	for k, v := range fields {
		obj[k] = v
	}
	return &unstructured.Unstructured{Object: obj}
}

func cond(t, s, msg string) map[string]any {
	return map[string]any{"type": t, "status": s, "message": msg, "lastTransitionTime": "2026-10-01T10:00:00Z"}
}

func hasFinding(s *report.Section, lvl report.Level, substr string) bool {
	for _, f := range s.Findings {
		if f.Level == lvl && strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------- etcd

const (
	idA uint64 = 11111111111111111111
	idB uint64 = 2222222222222222222
	idC uint64 = 3333333333333333333
)

func etcdPod(node string, ready bool) *corev1.Pod {
	st := corev1.ConditionTrue
	if !ready {
		st = corev1.ConditionFalse
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "etcd-" + node, Namespace: "openshift-etcd", Labels: map[string]string{"app": "etcd"}},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "etcd"}, {Name: "etcdctl"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}},
	}
}

func etcdHandler(healthyC bool, dbB int64) func(c execCall) (string, error) {
	return func(c execCall) (string, error) {
		cmd := strings.Join(c.cmd, " ")
		switch {
		case strings.Contains(cmd, "member list"):
			return fmt.Sprintf(`{"header":{},"members":[
 {"ID":%d,"name":"m0","clientURLs":["https://10.0.0.1:2379"]},
 {"ID":%d,"name":"m1","clientURLs":["https://10.0.0.2:2379"]},
 {"ID":%d,"name":"m2","clientURLs":["https://10.0.0.3:2379"]}]}`, idA, idB, idC), nil
		case strings.Contains(cmd, "endpoint status"):
			return fmt.Sprintf(`[
 {"Endpoint":"https://10.0.0.1:2379","Status":{"header":{"member_id":%[1]d},"version":"3.5.18","dbSize":209715200,"dbSizeInUse":104857600,"leader":%[2]d,"raftIndex":100}},
 {"Endpoint":"https://10.0.0.2:2379","Status":{"header":{"member_id":%[2]d},"version":"3.5.18","dbSize":%[4]d,"dbSizeInUse":104857600,"leader":%[2]d,"raftIndex":100}},
 {"Endpoint":"https://10.0.0.3:2379","Status":{"header":{"member_id":%[3]d},"version":"3.5.18","dbSize":110000000,"dbSizeInUse":104857600,"leader":%[2]d,"raftIndex":99}}]`,
				idA, idB, idC, dbB), nil
		case strings.Contains(cmd, "endpoint health"):
			return fmt.Sprintf(`[{"endpoint":"https://10.0.0.1:2379","health":true,"took":"8ms"},
 {"endpoint":"https://10.0.0.2:2379","health":true,"took":"9ms"},
 {"endpoint":"https://10.0.0.3:2379","health":%t,"took":"10ms"}]`, healthyC), nil
		case strings.Contains(cmd, "alarm list"):
			return "", nil
		case strings.Contains(cmd, "defrag"):
			return "Finished defragmenting etcd member[https://localhost:2379]", nil
		}
		return "", fmt.Errorf("unexpected command %q", cmd)
	}
}

func TestEtcdStateLeaderAndFragmentation(t *testing.T) {
	cs := fake.NewSimpleClientset(etcdPod("m0", true), etcdPod("m1", true), etcdPod("m2", true))
	env := &Env{CS: cs, Dyn: newDyn(), Exec: &fakeExec{handler: etcdHandler(true, 300<<20)}}
	st, err := GetEtcdState(context.Background(), env, "openshift-etcd")
	if err != nil {
		t.Fatal(err)
	}
	if st.Leader == nil || st.Leader.Name != "m1" || st.Leader.Pod != "etcd-m1" {
		t.Fatalf("leader = %+v, want m1", st.Leader)
	}
	if got := st.Members[0].Fragmentation(); got != 50 {
		t.Fatalf("fragmentation m0 = %v, want 50", got)
	}
	s := CheckEtcd(context.Background(), env, DefaultOptions())
	if s.Status != report.Warn {
		t.Fatalf("status = %v, want WARN (defrag recommended)", s.Status)
	}
	if !hasFinding(s, report.Warn, "Defragmentation recommended") {
		t.Fatalf("missing defrag recommendation: %+v", s.Findings)
	}
	if !strings.Contains(s.Summary, "leader: m1") {
		t.Fatalf("summary %q", s.Summary)
	}
}

func TestEtcdUnhealthyIsCritical(t *testing.T) {
	cs := fake.NewSimpleClientset(etcdPod("m0", true), etcdPod("m1", true), etcdPod("m2", true))
	env := &Env{CS: cs, Dyn: newDyn(), Exec: &fakeExec{handler: etcdHandler(false, 110<<20)}}
	s := CheckEtcd(context.Background(), env, DefaultOptions())
	if s.Status != report.Crit {
		t.Fatalf("status = %v, want CRIT", s.Status)
	}
}

func TestDefragOrderLeaderLast(t *testing.T) {
	cs := fake.NewSimpleClientset(etcdPod("m0", true), etcdPod("m1", true), etcdPod("m2", true))
	fe := &fakeExec{handler: etcdHandler(true, 300<<20)}
	env := &Env{CS: cs, Dyn: newDyn(), Exec: fe}
	s := ActionDefrag(context.Background(), env, DefaultOptions(), DefragOptions{CommandTimeout: time.Minute})
	if s.Status != report.Pass {
		t.Fatalf("status = %v findings=%+v", s.Status, s.Findings)
	}
	var order []string
	for _, c := range fe.calls {
		if strings.Contains(strings.Join(c.cmd, " "), " defrag") {
			order = append(order, c.pod)
			if c.cmd[0] != "env" || c.cmd[1] != "-u" || c.cmd[2] != "ETCDCTL_ENDPOINTS" {
				t.Fatalf("defrag must unset ETCDCTL_ENDPOINTS: %v", c.cmd)
			}
		}
	}
	want := []string{"etcd-m0", "etcd-m2", "etcd-m1"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("defrag order = %v, want %v", order, want)
	}
}

func TestDefragRefusedWhenUnhealthy(t *testing.T) {
	cs := fake.NewSimpleClientset(etcdPod("m0", true), etcdPod("m1", true), etcdPod("m2", true))
	fe := &fakeExec{handler: etcdHandler(false, 300<<20)}
	env := &Env{CS: cs, Dyn: newDyn(), Exec: fe}
	s := ActionDefrag(context.Background(), env, DefaultOptions(), DefragOptions{CommandTimeout: time.Minute})
	if s.Status != report.Error {
		t.Fatalf("status = %v, want ERROR", s.Status)
	}
	for _, c := range fe.calls {
		if strings.Contains(strings.Join(c.cmd, " "), " defrag") {
			t.Fatal("defrag executed on unhealthy cluster")
		}
	}
}

func TestEtcdSkippedOnHCP(t *testing.T) {
	env := &Env{CS: fake.NewSimpleClientset(), Dyn: newDyn(), HostedControlPlane: true}
	if s := CheckEtcd(context.Background(), env, DefaultOptions()); s.Status != report.Skip {
		t.Fatalf("status = %v, want SKIP", s.Status)
	}
}

// ------------------------------------------------------------- alerts

func TestAlerts(t *testing.T) {
	am := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "alertmanager-main-0", Namespace: "openshift-monitoring",
		Labels: map[string]string{"app.kubernetes.io/name": "alertmanager"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	fe := &fakeExec{handler: func(c execCall) (string, error) {
		cmd := strings.Join(c.cmd, " ")
		switch {
		case strings.Contains(cmd, "alert query"):
			return `[
 {"labels":{"alertname":"Watchdog","severity":"none"},"annotations":{},"startsAt":"2026-10-19T08:00:00Z","status":{"state":"active"}},
 {"labels":{"alertname":"KubePodCrashLooping","severity":"warning","namespace":"app"},"annotations":{"summary":"crash"},"startsAt":"2026-10-19T08:00:00Z","status":{"state":"active"}},
 {"labels":{"alertname":"etcdMembersDown","severity":"critical","namespace":"openshift-etcd"},"annotations":{"summary":"down"},"startsAt":"2026-10-19T09:00:00Z","status":{"state":"active"}}]`, nil
		case strings.Contains(cmd, "silence query"):
			return `[]`, nil
		case strings.Contains(cmd, "silence add"):
			return "abc-123\n", nil
		}
		return "", fmt.Errorf("unexpected %q", cmd)
	}}
	env := &Env{CS: fake.NewSimpleClientset(am), Dyn: newDyn(), Exec: fe}
	s := CheckAlerts(context.Background(), env, DefaultOptions())
	if s.Status != report.Crit {
		t.Fatalf("status = %v, want CRIT", s.Status)
	}
	if rows := s.Tables[0].Rows; len(rows) != 2 || rows[0][2] != "etcdMembersDown" {
		t.Fatalf("rows = %v", rows)
	}
	if fe.calls[0].container != "alertmanager" || fe.calls[0].pod != "alertmanager-main-0" {
		t.Fatalf("bad exec target %+v", fe.calls[0])
	}

	so := ActionSilence(context.Background(), env, DefaultOptions(), SilenceOptions{Duration: 2 * time.Hour, Author: "ben", Comment: "upgrade"})
	if !hasFinding(so, report.Pass, "UUID: abc-123") {
		t.Fatalf("silence id not reported: %+v", so.Findings)
	}
	last := fe.calls[len(fe.calls)-1].cmd
	if last[len(last)-1] != "alertname=~.+" {
		t.Fatalf("default matcher missing: %v", last)
	}
}

// -------------------------------------------------------------- PDB

func TestPDB(t *testing.T) {
	min := intstr.FromInt32(2)
	mk := func(name string, exp, cur, des, allowed int32) *policyv1.PodDisruptionBudget {
		return &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app"},
			Spec:       policyv1.PodDisruptionBudgetSpec{MinAvailable: &min},
			Status: policyv1.PodDisruptionBudgetStatus{ExpectedPods: exp, CurrentHealthy: cur,
				DesiredHealthy: des, DisruptionsAllowed: allowed},
		}
	}
	cs := fake.NewSimpleClientset(mk("ok", 3, 3, 2, 1), mk("blocking", 2, 2, 2, 0), mk("violated", 3, 1, 2, 0), mk("empty", 0, 0, 2, 0))
	s := CheckPDB(context.Background(), &Env{CS: cs}, DefaultOptions())
	if s.Status != report.Warn {
		t.Fatalf("status = %v", s.Status)
	}
	if !strings.Contains(s.Summary, "1 violated, 1 blocking drains") {
		t.Fatalf("summary %q", s.Summary)
	}
}

// -------------------------------------------------------------- MCP

func TestMCP(t *testing.T) {
	worker := u("machineconfiguration.openshift.io/v1", "MachineConfigPool", "", "worker", map[string]any{
		"spec": map[string]any{"paused": true, "nodeSelector": map[string]any{
			"matchLabels": map[string]any{"node-role.kubernetes.io/worker": ""}}},
		"status": map[string]any{"machineCount": int64(2), "readyMachineCount": int64(2), "updatedMachineCount": int64(2),
			"degradedMachineCount": int64(0), "conditions": []any{cond("Updating", "False", ""), cond("Degraded", "False", "")}},
	})
	master := u("machineconfiguration.openshift.io/v1", "MachineConfigPool", "", "master", map[string]any{
		"spec": map[string]any{"nodeSelector": map[string]any{
			"matchLabels": map[string]any{"node-role.kubernetes.io/master": ""}}},
		"status": map[string]any{"machineCount": int64(1), "readyMachineCount": int64(0), "updatedMachineCount": int64(0),
			"conditions": []any{cond("Updating", "True", ""), cond("Degraded", "False", "")}},
	})
	node := func(name string, lbl map[string]string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbl, Annotations: map[string]string{
			annMCState: "Done", annMCCurrent: "rendered-x", annMCDesired: "rendered-x"}}}
	}
	cs := fake.NewSimpleClientset(
		node("w1", map[string]string{"node-role.kubernetes.io/worker": ""}),
		node("m1", map[string]string{"node-role.kubernetes.io/master": ""}),
		node("orphan", map[string]string{"node-role.kubernetes.io/infra": ""}),
	)
	env := &Env{CS: cs, Dyn: newDyn(worker, master)}
	s := CheckMCP(context.Background(), env, DefaultOptions())
	if s.Status != report.Warn {
		t.Fatalf("status = %v findings=%+v", s.Status, s.Findings)
	}
	if !hasFinding(s, report.Warn, "PAUSED pools: worker") {
		t.Fatalf("paused pool not reported: %+v", s.Findings)
	}
	if !hasFinding(s, report.Warn, "Pools updating: master") {
		t.Fatalf("updating pool not reported: %+v", s.Findings)
	}
	if !hasFinding(s, report.Warn, "outside any pool: orphan") {
		t.Fatalf("orphan node not reported: %+v", s.Findings)
	}
}

// ------------------------------------------------------------ nodes

func TestNodesGrouping(t *testing.T) {
	mkNode := func(name, zone, tier string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
				"node-role.kubernetes.io/worker": "", "topology.kubernetes.io/zone": zone, "tier": tier}},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				Allocatable: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("250"),
					corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")},
			},
		}
	}
	mkPod := func(name, node string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app"},
			Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "c",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	}
	cs := fake.NewSimpleClientset(mkNode("n1", "z1", "gold"), mkNode("n2", "z2", "gold"), mkNode("n3", "z2", "silver"),
		mkPod("a", "n1"), mkPod("b", "n2"), mkPod("c", "n2"), mkPod("d", "n3"))
	o := DefaultOptions()
	o.GroupByLabels = []string{"tier"}
	s := CheckNodes(context.Background(), &Env{CS: cs}, o)
	if s.Status != report.Pass {
		t.Fatalf("status = %v %+v", s.Status, s.Findings)
	}
	find := func(title string) *report.Table {
		for _, tb := range s.Tables {
			if strings.HasPrefix(tb.Title, title) {
				return tb
			}
		}
		t.Fatalf("table %q missing", title)
		return nil
	}
	z := find("Per zone")
	if z.Rows[1][0] != "z2" || z.Rows[1][1] != "2" || z.Rows[1][2] != "3" {
		t.Fatalf("zone rows = %v", z.Rows)
	}
	tier := find("Per label tier")
	if tier.Rows[0][0] != "gold" || tier.Rows[0][2] != "3" {
		t.Fatalf("tier rows = %v", tier.Rows)
	}
	perNode := find("Per node")
	if perNode.Rows[1][6] != "50%" { // n2: 2 CPU requested / 4
		t.Fatalf("cpu%% n2 = %v", perNode.Rows[1])
	}
}

// --------------------------------------------------- cluster version

func TestClusterVersionTarget(t *testing.T) {
	cv := u("config.openshift.io/v1", "ClusterVersion", "", "version", map[string]any{
		"spec": map[string]any{"channel": "stable-4.18"},
		"status": map[string]any{
			"desired":          map[string]any{"version": "4.17.30"},
			"availableUpdates": []any{map[string]any{"version": "4.17.35"}, map[string]any{"version": "4.18.24"}},
			"conditionalUpdates": []any{map[string]any{"release": map[string]any{"version": "4.18.20"},
				"risks": []any{map[string]any{"name": "SomeBug"}}}},
			"conditions": []any{cond("Available", "True", ""), cond("Failing", "False", ""),
				cond("Progressing", "False", ""), cond("Upgradeable", "True", "")},
		},
	})
	env := &Env{Dyn: newDyn(cv)}
	o := DefaultOptions()

	o.TargetVersion = "4.18.24"
	if s := CheckClusterVersion(context.Background(), env, o); s.Status != report.Pass {
		t.Fatalf("recommended target: status %v %+v", s.Status, s.Findings)
	}
	o.TargetVersion = "4.18.20"
	if s := CheckClusterVersion(context.Background(), env, o); s.Status != report.Warn || !hasFinding(s, report.Warn, "SomeBug") {
		t.Fatalf("conditional target: status %v %+v", s.Status, s.Findings)
	}
	o.TargetVersion = "4.19.1"
	if s := CheckClusterVersion(context.Background(), env, o); s.Status != report.Crit {
		t.Fatalf("unknown target: status %v", s.Status)
	}
}

func TestClusterOperators(t *testing.T) {
	ok := u("config.openshift.io/v1", "ClusterOperator", "", "dns", map[string]any{"status": map[string]any{
		"conditions": []any{cond("Available", "True", ""), cond("Progressing", "False", ""), cond("Degraded", "False", "")},
		"versions":   []any{map[string]any{"name": "operator", "version": "4.17.30"}}}})
	bad := u("config.openshift.io/v1", "ClusterOperator", "", "ingress", map[string]any{"status": map[string]any{
		"conditions": []any{cond("Available", "True", ""), cond("Progressing", "False", ""), cond("Degraded", "True", "router KO")},
		"versions":   []any{map[string]any{"name": "operator", "version": "4.17.30"}}}})
	env := &Env{Dyn: newDyn(ok, bad), ClusterVersion: "4.17.30"}
	s := CheckClusterOperators(context.Background(), env, DefaultOptions())
	if s.Status != report.Crit || len(s.Tables[0].Rows) != 1 || s.Tables[0].Rows[0][0] != "ingress" {
		t.Fatalf("status %v rows %v", s.Status, s.Tables[0].Rows)
	}
}

func TestSemver(t *testing.T) {
	if !semverLess("4.9.10", "4.10.1") || semverLess("4.18.24", "4.18.3") {
		t.Fatal("semverLess broken")
	}
}
