# Operations runbook

This page describes how oc-precheck fits in an OpenShift upgrade and in a
datacenter maintenance. Commands are shown as `oc precheck`; use
`oc-precheck` if the binary is not installed as a plugin.

## 1. Cluster upgrade

### Days before

```bash
oc precheck --config precheck.yaml --target-version 4.18.24
```

- Fix every `CRIT`. Review the `WARN`: paused pools, blocking PDBs, quotas at
  100 %, operators not compatible with the target version.
- The `clusterversion` check tells you whether the target version is a
  recommended or a **conditional** update, and lists the known risks.
- The `olm` check lists operators with a pending manual InstallPlan. Check
  their compatibility with the target version in the Red Hat *Operator Update
  Information Checker*.

### Right before

1. Back up etcd and store the backup outside the cluster:
   ```bash
   oc debug node/<control-plane> -- chroot /host /usr/local/bin/cluster-backup.sh /home/core/backup
   ```
2. Run a dry run of the preparation actions:
   ```bash
   oc precheck --silence --silence-duration 4h \
               --banner "Upgrade to 4.18 in progress — CHG0012345" \
               --banner-link https://itsm.example.com/CHG0012345 \
               --defrag --dry-run
   ```
3. Then run them for real:
   ```bash
   oc precheck --silence --silence-duration 4h --banner "Upgrade to 4.18 in progress" --defrag --yes
   ```

What the actions do:

- `--silence` mutes every alert by default; restrict it with
  `--silence-matcher`. The UUID of the silence is printed.
- `--defrag` handles one member at a time, followers first and the leader
  last. Cluster health is checked again after each member, and the operation
  stops at the first problem.
- The actions are refused when a check is `CRIT`, unless `--force` is given.

### During

```bash
oc adm upgrade --to=4.18.24
oc precheck watch                 # Ctrl-C to quit
oc precheck watch --problems      # only nodes that are not nominal
```

The live view shows:

- the progress of the ClusterVersion (ClusterOperators at the target
  version, operators progressing or degraded);
- the MachineConfigPools;
- for each node: status, cordon, machine-config-daemon state and rendered
  configuration (`aaaaaaaa → bbbbbbbb` while updating, `✓ bbbbbbbb` when done).

### After

```bash
oc precheck --config precheck.yaml
oc precheck silence list            # UUIDs of the active silences
oc precheck silence expire <UUID>
oc precheck banner remove --all     # every banner created by oc-precheck
```

`--remove-banner` does the same as part of a `check` run.

## 2. Draining a room / zone

```bash
# 1. Analysis only
oc precheck drain -l topology.kubernetes.io/zone=room-a --dry-run
```

The analysis shows:

- the target nodes, with the pods to evict, to delete, and the skipped
  DaemonSet and static pods;
- the **blocking pods**:
  - pods without a controller would not be recreated (`--force` deletes
    them);
  - pods with an `emptyDir` volume would lose its data
    (`--delete-emptydir-data`);
- the **PodDisruptionBudgets** concerned: those allowing 0 disruptions block
  until replacement pods are ready elsewhere;
- the **remaining capacity per zone** on Ready, schedulable, non control
  plane nodes: pod slots, free CPU and memory (requests), compared with what
  has to move.

The drain is refused when:

- the selection contains control plane nodes (`--include-control-plane` to
  override; never drain more than one control plane node at a time);
- blocking pods remain;
- the remaining capacity looks insufficient (`--skip-capacity-check` to
  override).

```bash
# 2. Execute
oc precheck drain -l topology.kubernetes.io/zone=room-a --delete-emptydir-data --parallel 2

# 3. Follow
oc precheck watch -l topology.kubernetes.io/zone=room-a

# 4. After the maintenance
oc precheck uncordon -l topology.kubernetes.io/zone=room-a
```

Every target node is **cordoned first**, so that evicted pods do not land on
another node of the same room. The nodes are then drained, `--parallel` of
them at a time.

Evictions go through the eviction API, so PDBs are honoured:

- a refused eviction (HTTP 429) is retried every 5 seconds until
  `--node-timeout`;
- `--pod-parallel` controls the concurrent evictions per node;
- `--grace-period` overrides the pods' termination grace period;
- `--cordon-only` cordons without draining.

**Not simulated:** affinity, anti-affinity, topology spread constraints and
nodeSelectors. Review the workloads pinned to the zone being drained.

## 3. HyperShift hub

On a management cluster running HyperShift / MCE, the `hostedclusters` check
gives a global status per HostedCluster:

| Source | What is checked |
|---|---|
| HostedCluster conditions | `Available`, `Degraded`, `Progressing`, `EtcdAvailable`, `KubeAPIServerAvailable`, `ClusterVersionSucceeding`, `ClusterVersionUpgradeable`, `ValidReleaseImage`, `spec.pausedUntil` |
| Control plane namespace (`<namespace>-<name>`) | Pods not ready, hosted etcd pods ready |
| NodePools | `Ready`, `AllNodesHealthy`, version / config rollout, current vs desired replicas, version skew with the control plane |
| ACM | `ManagedClusterConditionAvailable` of the ManagedCluster with the same name |

```bash
oc precheck --checks hostedclusters
oc precheck --checks hostedclusters --hcp-filter 'prod-*'
oc precheck watch --hcp-filter 'prod-*'      # follow a control plane / NodePool upgrade
```

On a hosted (guest) cluster, the `etcd` and `mcp` checks are skipped. Their
equivalents live on the hub.

## 4. CI / automation

```bash
oc precheck --config precheck.yaml -o json > precheck.json
echo "exit code: $?"    # 0 OK, 1 warnings (--fail-on warn), 2 critical, 3 usage, 4 error
```

The JSON and YAML reports contain:

- every section, with its status, summary and findings (each finding carries
  its rule ID);
- the tables.

Use them to gate a pipeline or to archive the state of the cluster before a
change.
