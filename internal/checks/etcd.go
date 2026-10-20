// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/itlinuxfr/oc-upkit/internal/report"
)

// etcdctl v3.5 JSON shapes (`-w json`).
type etcdMemberList struct {
	Members []struct {
		ID         uint64   `json:"ID"`
		Name       string   `json:"name"`
		PeerURLs   []string `json:"peerURLs"`
		ClientURLs []string `json:"clientURLs"`
		IsLearner  bool     `json:"isLearner"`
	} `json:"members"`
}

type etcdEndpointStatus struct {
	Endpoint string `json:"Endpoint"`
	Status   struct {
		Header struct {
			MemberID uint64 `json:"member_id"`
		} `json:"header"`
		Version     string   `json:"version"`
		DBSize      int64    `json:"dbSize"`
		DBSizeInUse int64    `json:"dbSizeInUse"`
		Leader      uint64   `json:"leader"`
		RaftIndex   uint64   `json:"raftIndex"`
		RaftTerm    uint64   `json:"raftTerm"`
		IsLearner   bool     `json:"isLearner"`
		Errors      []string `json:"errors"`
	} `json:"Status"`
}

type etcdEndpointHealth struct {
	Endpoint string `json:"endpoint"`
	Health   bool   `json:"health"`
	Took     string `json:"took"`
	Error    string `json:"error"`
}

// EtcdMember is the consolidated view of one member.
type EtcdMember struct {
	ID          uint64
	Name        string // equals the node name on OpenShift
	Endpoint    string
	Pod         string
	PodReady    bool
	Restarts    int32
	Version     string
	DBSize      int64
	DBSizeInUse int64
	RaftIndex   uint64
	IsLeader    bool
	Healthy     bool
	HealthKnown bool
	Took        string
	Err         string
}

// Fragmentation returns the reclaimable share of the database in percent.
func (m EtcdMember) Fragmentation() float64 {
	if m.DBSize <= 0 {
		return 0
	}
	return float64(m.DBSize-m.DBSizeInUse) * 100 / float64(m.DBSize)
}

// EtcdState is the consolidated view of the cluster.
type EtcdState struct {
	Members []*EtcdMember
	Alarms  []string
	Leader  *EtcdMember
	ExecPod string
}

func etcdPods(ctx context.Context, env *Env, ns string) ([]corev1.Pod, error) {
	pods, err := env.CS.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=etcd"})
	if err != nil {
		return nil, err
	}
	var out []corev1.Pod
	for _, p := range pods.Items {
		for _, c := range p.Spec.Containers {
			if c.Name == "etcdctl" {
				out = append(out, p)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func podReady(p corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podRestarts(p corev1.Pod) int32 {
	var n int32
	for _, cs := range p.Status.ContainerStatuses {
		n += cs.RestartCount
	}
	return n
}

func etcdctl(ctx context.Context, env *Env, ns, pod string, args ...string) (string, error) {
	out, _, err := env.Exec.Exec(ctx, ns, pod, "etcdctl", append([]string{"etcdctl"}, args...))
	return out, err
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// GetEtcdState queries etcd through the etcdctl container of the first
// ready etcd pod, whose environment already holds endpoints and certificates.
func GetEtcdState(ctx context.Context, env *Env, ns string) (*EtcdState, error) {
	pods, err := etcdPods(ctx, env, ns)
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		return nil, fmt.Errorf("no etcd pod (label app=etcd with an etcdctl container) in %s", ns)
	}
	byNode := map[string]corev1.Pod{}
	execPod := ""
	for _, p := range pods {
		byNode[p.Spec.NodeName] = p
		if execPod == "" && podReady(p) {
			execPod = p.Name
		}
	}
	if execPod == "" {
		execPod = pods[0].Name
	}
	st := &EtcdState{ExecPod: execPod}

	out, err := etcdctl(ctx, env, ns, execPod, "member", "list", "-w", "json")
	if err != nil {
		return nil, err
	}
	var ml etcdMemberList
	if err := json.Unmarshal([]byte(out), &ml); err != nil {
		return nil, fmt.Errorf("decoding member list: %w", err)
	}
	byEndpoint := map[string]*EtcdMember{}
	byID := map[uint64]*EtcdMember{}
	for _, m := range ml.Members {
		em := &EtcdMember{ID: m.ID, Name: m.Name}
		if len(m.ClientURLs) > 0 {
			em.Endpoint = m.ClientURLs[0]
			for _, u := range m.ClientURLs {
				byEndpoint[hostOf(u)] = em
			}
		}
		if p, ok := byNode[m.Name]; ok {
			em.Pod, em.PodReady, em.Restarts = p.Name, podReady(p), podRestarts(p)
		}
		st.Members = append(st.Members, em)
		byID[m.ID] = em
	}

	// `endpoint status` exits non-zero when a member is down, but stdout
	// still carries the reachable members, so it is parsed anyway.
	out, statusErr := etcdctl(ctx, env, ns, execPod, "endpoint", "status", "--cluster", "-w", "json")
	var es []etcdEndpointStatus
	if json.Unmarshal([]byte(out), &es) == nil {
		for _, e := range es {
			m := byID[e.Status.Header.MemberID]
			if m == nil {
				m = byEndpoint[hostOf(e.Endpoint)]
			}
			if m == nil {
				continue
			}
			m.Version = e.Status.Version
			m.DBSize = e.Status.DBSize
			m.DBSizeInUse = e.Status.DBSizeInUse
			m.RaftIndex = e.Status.RaftIndex
			m.IsLeader = e.Status.Leader == e.Status.Header.MemberID
			if m.IsLeader {
				st.Leader = m
			}
		}
	} else if statusErr != nil {
		return nil, statusErr
	}

	out, _ = etcdctl(ctx, env, ns, execPod, "endpoint", "health", "--cluster", "-w", "json")
	var eh []etcdEndpointHealth
	if json.Unmarshal([]byte(out), &eh) == nil {
		for _, h := range eh {
			if m := byEndpoint[hostOf(h.Endpoint)]; m != nil {
				m.Healthy, m.HealthKnown, m.Took, m.Err = h.Health, true, h.Took, h.Error
			}
		}
	}

	if out, err := etcdctl(ctx, env, ns, execPod, "alarm", "list"); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				st.Alarms = append(st.Alarms, l)
			}
		}
	}
	return st, nil
}

// CheckEtcd reports member health, leader, database size and fragmentation.
func CheckEtcd(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("etcd", "etcd: health, leader, fragmentation")
	if env.HostedControlPlane {
		return s.Skipped("hosted control plane: etcd runs in the HCP namespace of the management cluster (use the hostedclusters check on the hub)")
	}
	r := newRuler(s, o)
	ns := o.EtcdNamespace

	// The operator view is cheap and works even when exec is denied.
	if eo, err := env.Dyn.Resource(gvrEtcdOperator).Get(ctx, "cluster", metaGet); err == nil {
		c := conditions(eo.Object)
		if m := c["EtcdMembersAvailable"].Message; m != "" {
			s.Add(report.Info, "etcd operator: %s", m)
		}
		if isTrue(c, "EtcdMembersDegraded") {
			r.add("etcd.members-degraded", ns, "cluster", "EtcdMembersDegraded: %s", oneLine(c["EtcdMembersDegraded"].Message, 200))
		}
	}

	st, err := GetEtcdState(ctx, env, ns)
	if err != nil {
		return s.Fail(err, "pods/exec permission required in "+ns)
	}

	t := s.NewTable("", "Member", "Pod", "Leader", "Health", "Latency", "Version", "DB size", "In use", "Frag.", "Raft index", "Restarts")
	var healthy int
	var defragNeeded []string
	for _, m := range st.Members {
		lvl := report.Pass
		bump := func(l report.Level, kept bool) {
			if kept {
				lvl = report.Worse(lvl, l)
			}
		}
		health := "?"
		switch {
		case m.HealthKnown && m.Healthy:
			health = "OK"
			healthy++
		case m.HealthKnown:
			health = "KO"
			bump(r.add("etcd.member-unhealthy", ns, m.Name, "Member %s is unhealthy: %s", m.Name, m.Err))
		default:
			bump(r.add("etcd.member-unhealthy", ns, m.Name, "Member %s health unknown (endpoint unreachable)", m.Name))
		}
		if m.Pod == "" {
			bump(r.add("etcd.pod-missing", ns, m.Name, "No etcd pod found on node %s", m.Name))
		} else if !m.PodReady {
			bump(r.add("etcd.pod-not-ready", ns, m.Pod, "Pod %s is not Ready", m.Pod))
		}
		frag := m.Fragmentation()
		if frag >= o.EtcdFragThreshold && m.DBSize >= o.EtcdMinDefragSize {
			if l, kept := r.eval("etcd.fragmentation", ns, m.Name); kept {
				lvl = report.Worse(lvl, l)
				defragNeeded = append(defragNeeded, fmt.Sprintf("%s (%.0f%%)", m.Name, frag))
			}
		}
		if o.EtcdDBSizeWarn > 0 && m.DBSize > o.EtcdDBSizeWarn {
			bump(r.add("etcd.db-size", ns, m.Name, "Database of member %s is %s (threshold %s, default quota 8 GiB)",
				m.Name, humanBytes(m.DBSize), humanBytes(o.EtcdDBSizeWarn)))
		}
		leader := ""
		if m.IsLeader {
			leader = "★"
		}
		t.AddRow(lvl, m.Name, orDash(m.Pod), leader, health, orDash(m.Took), orDash(m.Version),
			humanBytes(m.DBSize), humanBytes(m.DBSizeInUse), fmt.Sprintf("%.0f%%", frag),
			fmt.Sprint(m.RaftIndex), fmt.Sprint(m.Restarts))
	}

	if st.Leader != nil {
		s.Summary = fmt.Sprintf("%d/%d healthy members — leader: %s (%s)", healthy, len(st.Members), st.Leader.Name, st.Leader.Pod)
	} else {
		s.Summary = fmt.Sprintf("%d/%d healthy members — NO LEADER found", healthy, len(st.Members))
		r.add("etcd.no-leader", ns, "", "No etcd leader found")
	}
	if o.EtcdExpectedMembers > 0 && len(st.Members) != o.EtcdExpectedMembers {
		r.add("etcd.member-count", ns, "", "%d etcd members (%d expected)", len(st.Members), o.EtcdExpectedMembers)
	}
	if versions := distinct(st.Members, func(m *EtcdMember) string { return m.Version }); len(versions) > 1 {
		r.add("etcd.version-mismatch", ns, "", "etcd members run different versions: %s", strings.Join(versions, ", "))
	}
	for _, a := range st.Alarms {
		r.add("etcd.alarm", ns, "", "Active etcd alarm: %s", a)
	}
	if len(defragNeeded) > 0 {
		r.add("etcd.fragmentation", ns, "", "Defragmentation recommended (≥ %.0f%% reclaimable): %s — see `oc upkit defrag`",
			o.EtcdFragThreshold, strings.Join(defragNeeded, ", "))
	}
	s.Add(report.Info, "Take an etcd backup (cluster-backup.sh) right before the upgrade and store it outside the cluster")
	r.done()
	return s
}

func distinct(ms []*EtcdMember, f func(*EtcdMember) string) []string {
	seen := map[string]bool{}
	for _, m := range ms {
		if v := f(m); v != "" {
			seen[v] = true
		}
	}
	return sortedKeys(seen)
}

// ------------------------------------------------------------- defrag

// DefragOptions configures the defragmentation operation.
type DefragOptions struct {
	MinFragmentation float64       // skip members below this percentage
	Pause            time.Duration // wait between members
	CommandTimeout   time.Duration
	DisarmAlarms     bool
	DryRun           bool
	Log              io.Writer // live progress
}

// ActionDefrag defragments members one at a time, followers first and the
// leader last, checking cluster health between members.
func ActionDefrag(ctx context.Context, env *Env, o *Options, do DefragOptions) *report.Section {
	s := report.NewSection("defrag", "Action: etcd defragmentation")
	if env.HostedControlPlane {
		return s.Skipped("hosted control plane: no etcd in this cluster")
	}
	logf := func(format string, a ...any) {
		if do.Log != nil {
			fmt.Fprintf(do.Log, "  [defrag] "+format+"\n", a...)
		}
	}
	st, err := GetEtcdState(ctx, env, o.EtcdNamespace)
	if err != nil {
		return s.Fail(err, "")
	}
	for _, m := range st.Members {
		if !m.HealthKnown || !m.Healthy || m.Pod == "" || !m.PodReady {
			return s.Fail(fmt.Errorf("member %s is unhealthy or has no Ready pod", m.Name),
				"defragmentation refused: restore etcd health first")
		}
	}

	order := make([]*EtcdMember, 0, len(st.Members))
	for _, m := range st.Members {
		if !m.IsLeader {
			order = append(order, m)
		}
	}
	if st.Leader != nil {
		order = append(order, st.Leader)
	}

	t := s.NewTable("", "Order", "Member", "Pod", "Leader", "Before", "After", "Reclaimed", "Result")
	var done int
	for i, m := range order {
		role := ""
		if m.IsLeader {
			role = "★"
		}
		frag := m.Fragmentation()
		if frag < do.MinFragmentation {
			t.AddRow(report.Skip, fmt.Sprint(i+1), m.Name, m.Pod, role, humanBytes(m.DBSize), "-", "-",
				fmt.Sprintf("skipped (%.0f%% < %.0f%%)", frag, do.MinFragmentation))
			continue
		}
		if do.DryRun {
			t.AddRow(report.Info, fmt.Sprint(i+1), m.Name, m.Pod, role, humanBytes(m.DBSize), "-", "-", "dry run")
			continue
		}
		logf("%d/%d %s (%s) — %s, fragmentation %.0f%%", i+1, len(order), m.Name, m.Pod, humanBytes(m.DBSize), frag)

		// ETCDCTL_ENDPOINTS must be unset, otherwise etcdctl rejects the
		// --endpoints flag (same as the Red Hat procedure).
		cmd := []string{"env", "-u", "ETCDCTL_ENDPOINTS", "etcdctl",
			fmt.Sprintf("--command-timeout=%s", do.CommandTimeout),
			"--endpoints=https://localhost:2379", "defrag"}
		out, _, err := env.Exec.Exec(ctx, o.EtcdNamespace, m.Pod, "etcdctl", cmd)
		if err != nil {
			t.AddRow(report.Crit, fmt.Sprint(i+1), m.Name, m.Pod, role, humanBytes(m.DBSize), "-", "-", "FAILED")
			return s.Fail(err, "defragmentation interrupted")
		}
		logf("%s", strings.TrimSpace(out))

		if do.Pause > 0 && i < len(order)-1 {
			logf("waiting %s before the next member", do.Pause)
			select {
			case <-ctx.Done():
				return s.Fail(ctx.Err(), "")
			case <-time.After(do.Pause):
			}
		}
		after, err := GetEtcdState(ctx, env, o.EtcdNamespace)
		if err != nil {
			return s.Fail(err, "etcd state unreadable after defragmentation: stopping")
		}
		var am *EtcdMember
		for _, x := range after.Members {
			if x.ID == m.ID {
				am = x
			}
			if !x.HealthKnown || !x.Healthy {
				t.AddRow(report.Crit, fmt.Sprint(i+1), m.Name, m.Pod, role, humanBytes(m.DBSize), "-", "-", "cluster unhealthy")
				return s.Fail(fmt.Errorf("member %s unhealthy after defragmenting %s", x.Name, m.Name), "stopping")
			}
		}
		newSize := int64(0)
		if am != nil {
			newSize = am.DBSize
		}
		t.AddRow(report.Pass, fmt.Sprint(i+1), m.Name, m.Pod, role, humanBytes(m.DBSize), humanBytes(newSize),
			humanBytes(m.DBSize-newSize), "OK")
		done++
	}

	if do.DryRun {
		s.Summary = fmt.Sprintf("Plan: %d member(s), leader %s last", len(order), leaderName(st))
		return s
	}
	s.Summary = fmt.Sprintf("%d member(s) defragmented, leader %s last", done, leaderName(st))

	// NOSPACE alarms can remain after a defragmentation and must be disarmed.
	if out, err := etcdctl(ctx, env, o.EtcdNamespace, st.ExecPod, "alarm", "list"); err == nil && strings.TrimSpace(out) != "" {
		if do.DisarmAlarms {
			if _, err := etcdctl(ctx, env, o.EtcdNamespace, st.ExecPod, "alarm", "disarm"); err != nil {
				s.Add(report.Crit, "Alarms present and disarm failed: %v", err)
			} else {
				s.Add(report.Warn, "Alarms disarmed: %s", oneLine(out, 200))
			}
		} else {
			s.Add(report.Crit, "etcd alarms present after defragmentation: %s (re-run with --disarm-alarms)", oneLine(out, 200))
		}
	}
	return s
}

func leaderName(st *EtcdState) string {
	if st.Leader == nil {
		return "?"
	}
	return st.Leader.Name
}
