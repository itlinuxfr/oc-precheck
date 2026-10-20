# oc-upkit

[![CI](https://github.com/itlinuxfr/oc-upkit/actions/workflows/ci.yml/badge.svg)](https://github.com/itlinuxfr/oc-upkit/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/itlinuxfr/oc-upkit)](https://goreportcard.com/report/github.com/itlinuxfr/oc-upkit)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

**Upgrade toolkit for OpenShift 4: pre-upgrade checks and maintenance operations, as an `oc` plugin.**

`oc upkit` tells you whether a cluster is ready to be upgraded, and helps you
run everything around the upgrade or a datacenter maintenance:

```console
$ oc upkit --target-version 4.18.24
oc-upkit — OpenShift pre-upgrade checks
  Context  : admin/api-prod-example-com:6443
  Version  : 4.17.30
  Channel  : stable-4.18
──────────────────────────────────────────────────────────────────────────────
[PASS ] ClusterVersion and update path
        Version 4.17.30 — channel "stable-4.18"
   ✓ Target version 4.18.24: recommended path from 4.17.30
──────────────────────────────────────────────────────────────────────────────
[WARN ] etcd: health, leader, fragmentation
        3/3 healthy members — leader: master-1 (etcd-master-1)
   ! Defragmentation recommended (≥ 45% reclaimable): master-0 (52%)
...
══════════════════════════════════════════════════════════════════════════════
Summary
  [PASS ] ClusterVersion and update path
  [CRIT ] Firing alerts (Alertmanager)
  [WARN ] etcd: health, leader, fragmentation
  [WARN ] PodDisruptionBudgets
  ...
[CRIT ] CRITICAL issues: do not start the upgrade as is.
```

## Features

- **15 pre-upgrade checks**, all read-only:
  - ClusterVersion and update graph (validates `--target-version`);
  - firing alerts and active silences;
  - etcd: leader, health, fragmentation;
  - ClusterOperators and OLM operators;
  - MachineConfigPools (paused, updating, nodes outside any pool);
  - PodDisruptionBudgets that will block drains;
  - nodes and pod distribution per zone or label;
  - unhealthy pods (`0/1 Running`, `CrashLoopBackOff`…);
  - running Jobs and quotas;
  - pending CSRs;
  - HostedClusters / NodePools seen from a HyperShift hub.
- **Configurable rules.** Each of the 84 built-in rules has a stable ID. You
  can change its severity (`crit`, `warn`, `info`, `off`) and exclude
  namespaces or objects in a YAML file. You can also add your **own rules in
  CEL**, the expression language of ValidatingAdmissionPolicies.
- **Maintenance operations:**
  - Alertmanager silences, shown with their UUID;
  - web console banner;
  - etcd defragmentation, leader last;
  - **drain of a whole room / zone by label**, with a pre-flight capacity
    and PDB analysis;
  - **live node view** during the upgrade.
- **CI friendly:**
  - `-o json|yaml` output;
  - meaningful exit codes;
  - every setting available as a flag or in a config file;
  - a static binary for Linux (RHEL 8/9), macOS and Windows.
- **Kubernetes-native.** It uses the same kubeconfig loading and connection
  flags as `oc`/`kubectl` (`--context`, `--server`, `--token`, `--as`…), and
  installs as an `oc` / `kubectl` plugin.

> On OpenShift 4.20+, `oc adm upgrade recommend` performs some update-risk
> checks. oc-upkit is complementary: it covers the cluster health items of
> the Red Hat "upgrade pre-checks" knowledge article and adds configurable
> rules and maintenance operations.

## Installation

### As an `oc` / `kubectl` plugin (recommended)

`oc` and `kubectl` run any executable named `oc-<name>` or `kubectl-<name>`
found in the `PATH` as `oc <name>`.

```bash
VERSION=v0.3.0
OS=linux ARCH=amd64          # linux|darwin|windows, amd64|arm64
curl -sSLO "https://github.com/itlinuxfr/oc-upkit/releases/download/${VERSION}/oc-upkit_${VERSION#v}_${OS}_${ARCH}.tar.gz"
tar xzf "oc-upkit_${VERSION#v}_${OS}_${ARCH}.tar.gz" oc-upkit
install -m 0755 oc-upkit ~/.local/bin/oc-upkit            # or /usr/local/bin
ln -sf ~/.local/bin/oc-upkit ~/.local/bin/kubectl-upkit   # optional: kubectl upkit

oc plugin list
oc upkit version
```

On Windows, put `oc-upkit.exe` in a folder of your `PATH`.

### With Go

```bash
go install github.com/itlinuxfr/oc-upkit/cmd/oc-upkit@latest
```

### From source

```bash
git clone https://github.com/itlinuxfr/oc-upkit && cd oc-upkit
make build            # ./bin/oc-upkit
make plugin-install   # copies it to ~/.local/bin as oc-upkit and kubectl-upkit
```

See [docs/installation.md](docs/installation.md) for checksums, air-gapped
installs and RHEL notes.

## Quick start

```bash
oc login https://api.my-cluster.example.com:6443

oc upkit                                  # all checks (same as "oc upkit check")
oc upkit --checks etcd,mcp,pdb --verbose  # a subset, healthy items included
oc upkit --target-version 4.18.24         # validate the update path too
oc upkit -o json > upkit.json          # machine-readable report

oc upkit config default > upkit.yaml   # every setting and rule, with defaults
oc upkit --config upkit.yaml
```

## Commands

| Command | Description |
|---|---|
| `oc upkit run [SCENARIO\|FILE]` | Run a scenario (`pre-upgrade`, `post-upgrade` or your own file): checks and actions step by step, with a gate after each step. |
| `oc upkit [check]` | Run the pre-upgrade checks (default command). Optional actions: `--silence`, `--banner`, `--remove-banner`, `--defrag`. |
| `oc upkit watch` | Live view: nodes (Ready, cordon, MCD state, rendered config current → target, pods), MCPs, cluster update progress; HostedClusters/NodePools on a hub. |
| `oc upkit drain -l SELECTOR` | Pre-flight analysis, then cordon and drain every node matching a label selector (e.g. a room). |
| `oc upkit uncordon -l SELECTOR` | Put nodes back in service. |
| `oc upkit banner set\|remove\|list` | Web console banner (ConsoleNotification). |
| `oc upkit silence list\|add\|expire` | Alertmanager silences. |
| `oc upkit defrag` | etcd defragmentation, one member at a time, leader last. |
| `oc upkit rules` | Rule catalog with default and effective severity. |
| `oc upkit config default\|validate` | Generate the default configuration / validate a file. |
| `oc upkit version` | Version and build information. |
| `oc upkit completion bash\|zsh\|fish\|powershell` | Shell completion. |

Every command accepts the standard connection flags (`--kubeconfig`,
`--context`, `--cluster`, `--user`, `--server`, `--token`, `--as`,
`--as-group`, `--certificate-authority`, `--insecure-skip-tls-verify`,
`--request-timeout`…) and the global flags `--config`, `-o text|json|yaml`,
`--verbose`, `--max-rows`, `--no-color` and `--timeout`. Commands that change
the cluster ask for confirmation and accept `--dry-run` and `-y/--yes`.

Full reference: [docs/cli](docs/cli/oc_upkit.md).

## Checks

| Check | What it verifies |
|---|---|
| `clusterversion` | Conditions (Failing, Progressing, Upgradeable…), channel, recommended and conditional updates with their risks, unmanaged overrides, `--target-version` |
| `alerts` | Firing alerts by severity (silenced/inhibited ones as info), through `amtool` in the `alertmanager-main` pod |
| `silences` | Active silences with their **UUID**, author, end and remaining time; warns when a silence mutes everything |
| `etcd` | Members, **leader**, health and latency, version, database size, **fragmentation**, raft index, restarts, alarms |
| `clusteroperators` | Available / Degraded / Progressing / Upgradeable, version different from the cluster version |
| `olm` | CSVs, Subscriptions (manual InstallPlan pending, ResolutionFailed…), CatalogSources |
| `hostedclusters` | **HyperShift hub**: global status per HostedCluster (control plane conditions, hosted etcd and API server, control plane pods, NodePools, ACM registration) |
| `mcp` | Paused / updating / degraded pools; nodes outside any pool or in several custom pools; machine-config-daemon state |
| `pdb` | PDBs violated or allowing 0 disruptions (they will block drains) |
| `nodes` | Status, pressure, pods per node / zone / role / **custom label**, CPU and memory requests, kubelet versions |
| `pods` | `READY x/y` like `oc get pods`: Running `0/1`, `CrashLoopBackOff`, `ImagePullBackOff`, `CreateContainerConfigError`, `Init:…`, Pending, stuck Terminating, Unknown; Failed/Evicted and restarts counted |
| `jobs` | **Running Jobs**: originating CronJob, start, **duration**, completions, nodes |
| `quotas` | ResourceQuotas and ClusterResourceQuotas used at **≥ 90 %** (configurable) on at least one resource |
| `csr` | Pending CertificateSigningRequests |
| `custom` | Your CEL rules from the configuration file |

On a hosted cluster (HCP guest), `etcd` and `mcp` are skipped. Off a hub,
`hostedclusters` is skipped. The rule catalog is in
[docs/rules.md](docs/rules.md).

## Configuration

```bash
oc upkit config default > upkit.yaml          # start from the defaults
oc upkit config validate --config upkit.yaml  # exit code 3 when invalid
oc upkit --config upkit.yaml                  # or UPKIT_CONFIG=upkit.yaml
```

Precedence: built-in defaults < configuration file < command-line flags.

```yaml
settings:               # any command-line setting
  quotaThreshold: 90
  groupByLabels: [topology.kubernetes.io/zone]
severities:             # per rule ID, globs accepted: crit | warn | info | "off"
  mcp.paused: info      # EUS upgrade: worker pools paused on purpose
  pdb.blocking: crit
exclusions:             # ignore objects for some rules
  - rule: "pods.*"
    namespaces: ["sandbox-*"]
customRules:            # your own rules, in CEL
  - id: org.olm-manual-approval
    apiVersion: operators.coreos.com/v1alpha1
    resource: subscriptions
    expr: 'object.spec.installPlanApproval == "Manual"'
    messageExpression: '"Subscription " + object.spec.name + " uses Automatic approval"'
```

Custom rules work on any resource the API server serves, per object or on
the whole list (`aggregate: true`). They can use `params`, `now`,
`clusterVersion`, and the CEL strings, lists, sets and math libraries.

- Reference: [docs/configuration.md](docs/configuration.md).
- Annotated example: [examples/upkit.yaml](examples/upkit.yaml).

## Typical maintenance workflow

```bash
# Before
oc upkit --config upkit.yaml --target-version 4.18.24   # read-only checks
oc upkit run pre-upgrade --dry-run                       # show the plan
oc upkit run pre-upgrade                                 # checks, silence, banner, defrag

# During: the upgrade is yours to start, oc-upkit never does it
oc adm upgrade --to=4.18.24
oc upkit watch

# After
oc upkit run post-upgrade                                # checks, then banner and silences removed
```

## Scenarios

A scenario chains the checks and operations of the tool, with a **gate**
after each step: a critical (or failed) step stops the scenario before
anything else changes, a step with warnings asks for confirmation. The plan
is shown first, and nothing runs before you confirm it.

| Built-in | Steps |
|---|---|
| `pre-upgrade` | all checks → silence (4h) → console banner → etcd defragmentation → checks `etcd`, `pods` |
| `post-upgrade` | all checks → remove the banner → expire the silences created by oc-upkit |

To adapt one to your site, export it, edit it, and run the file:

```bash
oc upkit run pre-upgrade --print > my-pre-upgrade.yaml
oc upkit run my-pre-upgrade.yaml -o json > pre-upgrade-report.json
```

```yaml
name: my-pre-upgrade
steps:
  - check: {}
  - silence: {duration: 4h, comment: "Upgrade to 4.18 — CHG0012345"}
  - banner: {text: "Upgrade to 4.18 in progress", link: "https://itsm.example.com/CHG0012345"}
  - defrag: {}
  - job: {cronJob: openshift-etcd/etcd-backup, timeout: 1h}   # site-specific backup
  - check: {checks: [etcd, pods]}
```

A scenario only uses the operations of the tool (no shell commands) and
**never starts the upgrade**: that decision stays with the operator. With
`-o json`, the report records who ran the scenario, when, and the result of
every step. Reference: [docs/scenarios.md](docs/scenarios.md).

The full runbook is [docs/operations.md](docs/operations.md). It covers
draining a room and the HyperShift hub workflow.

### Draining a room

```bash
oc upkit drain -l topology.kubernetes.io/zone=room-a --dry-run     # analysis only
oc upkit drain -l topology.kubernetes.io/zone=room-a --delete-emptydir-data --parallel 2
oc upkit watch -l topology.kubernetes.io/zone=room-a
oc upkit uncordon -l topology.kubernetes.io/zone=room-a
```

Before changing anything, the drain shows:

- the pods to evict;
- the **blocking pods** (no controller, emptyDir);
- the **affected PDBs**;
- the **remaining capacity per zone**.

It refuses to run when:

- the selection contains control plane nodes;
- blocking pods remain;
- the remaining capacity looks insufficient.

Every target node is cordoned **before** draining, so that pods do not land in
the same room. The drain then uses the eviction API, which honors PDBs.

## Output and exit codes

`-o text` (default, colored on a terminal), `-o json` or `-o yaml`.

| Code | Meaning |
|---|---|
| 0 | No blocking issue |
| 1 | Warnings (only with `--fail-on warn` or `settings.failOn: warn`) |
| 2 | Critical issues |
| 3 | Usage or configuration error |
| 4 | Execution error (API unreachable, a check could not run) |

## Permissions

- The read-only checks need list/get access to nodes, pods, PDBs, CSRs,
  quotas, jobs and the OpenShift config, MCO, OLM and HyperShift APIs.
- `alerts`, `silences`, `etcd` and `defrag` need `pods/exec` in
  `openshift-monitoring` and `openshift-etcd`.
- The operations also need to patch nodes, create `pods/eviction`, and manage
  `consolenotifications`.

Ready-made ClusterRoles are in [examples/rbac.yaml](examples/rbac.yaml).
Without a permission, the affected check reports an `ERROR` and the others
still run.

## Compatibility

- Designed for OpenShift 4.12 and later, including HyperShift / hosted
  control planes (from the hub and from the hosted cluster). The project is
  young: the test suite runs against fake clients, so please report any
  behavior you see on a real cluster.
- Plain Kubernetes works for the generic checks (`nodes`, `pods`, `pdb`,
  `jobs`, `quotas`, `csr`, custom rules, drain, watch). The OpenShift-specific
  checks report `SKIP`.

## Development

```bash
make test        # unit tests (fake clientsets, no cluster needed)
make lint        # golangci-lint
make docs        # regenerate docs/cli, docs/rules.md and docs/default-config.yaml
make cross       # binaries for linux/darwin/windows × amd64/arm64 in dist/
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[Apache License 2.0](LICENSE).

OpenShift is a trademark of Red Hat, Inc. This project is not affiliated with,
endorsed or supported by Red Hat.
