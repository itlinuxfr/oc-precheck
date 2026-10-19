# Configuration reference

oc-precheck reads an optional YAML file given with `--config` (or the
`OCP_PRECHECK_CONFIG` environment variable).

```bash
oc precheck config default > precheck.yaml          # every setting and rule with its default
oc precheck config validate --config precheck.yaml  # exit code 3 when invalid
oc precheck rules --config precheck.yaml            # effective severity of every rule
```

**Precedence:** built-in defaults < configuration file < command-line flags.
A flag given explicitly always wins over the file.

Unknown keys are rejected, which catches typos early.

The file has four sections: `settings`, `severities`, `exclusions` and
`customRules`. The generated [default configuration](default-config.yaml)
lists all of them with their default values.

## settings

| Key | Flag | Default | Description |
|---|---|---|---|
| `failOn` | `--fail-on` | `crit` | Level giving a non-zero exit code: `warn` or `crit` |
| `checks` | `--checks` | all | Checks to run |
| `skip` | `--skip` | none | Checks to skip |
| `maxRows` | `--max-rows` | `50` | Maximum rows per table (0 = unlimited) |
| `targetVersion` | `--target-version` | none | Version validated against the update graph |
| `alertSeverities` | `--alert-severities` | `[critical, warning]` | Alert severities to report |
| `ignoreAlerts` | `--ignore-alerts` | `[Watchdog, AlertmanagerReceiversNotConfigured, InfoInhibitor]` | Alerts ignored by name |
| `alertmanagerURL` | `--alertmanager-url` | `http://localhost:9093` | Alertmanager URL as seen from its pod |
| `monitoringNamespace` | `--monitoring-namespace` | `openshift-monitoring` | Namespace of the platform monitoring stack |
| `etcdNamespace` | `--etcd-namespace` | `openshift-etcd` | Namespace of the etcd pods |
| `etcdFragThreshold` | `--etcd-frag-threshold` | `45` | Fragmentation (%) above which a defragmentation is recommended |
| `etcdDBSizeWarn` | `--etcd-db-size-warn` | `6Gi` | Database size above which to warn |
| `etcdExpectedMembers` | `--etcd-expected-members` | `3` | Expected number of etcd members |
| `zoneLabel` | `--zone-label` | `topology.kubernetes.io/zone` | Node label holding the zone / room |
| `groupByLabels` | `--group-by-label` | none | Node labels used to group the pod count |
| `nodeSelector` | `--node-selector` | none | Restrict the `nodes` check |
| `nodePodCapacityPct` | `--node-pod-capacity` | `90` | Pod capacity usage (%) per node above which to warn |
| `podPendingAge` | `--pod-pending-age` | `5m` | Report Pending pods older than this |
| `podNotReadyAge` | `--pod-notready-age` | `0s` | Report Running pods not ready for longer than this (0 = immediately) |
| `podTerminatingAge` | `--pod-terminating-age` | `10m` | Report pods Terminating for longer than this |
| `podRestartWarn` | `--pod-restart-warn` | `10` | Restart count reported as information |
| `quotaThreshold` | `--quota-threshold` | `90` | Quota usage (%) above which to warn |
| `jobLongDuration` | `--job-long-duration` | `1h` | Warn about Jobs running longer than this |
| `hcpFilter` | `--hcp-filter` | none | HostedClusters to include (globs on `name` or `namespace/name`) |

Durations use the Go syntax (`90s`, `15m`, `2h`). Sizes use Kubernetes
quantities (`6Gi`).

## severities

Every built-in rule has a stable ID: `<check>.<problem>`, for example
`pdb.blocking`. Each rule has a default severity:

- `crit` makes the run fail (exit code 2);
- `warn` needs attention (exit code 1 with `failOn: warn`);
- `info` is shown but never changes the status;
- `off` disables the rule.

```yaml
severities:
  mcp.paused: info          # EUS upgrade: worker pools paused on purpose
  pdb.blocking: crit        # a blocking PDB is a no-go here
  "pods.*": info            # globs are accepted; the most specific pattern wins
  pods.waiting-error: warn  # ...so this one stays warn
  pods.failed: "off"
```

Quote `"off"`. Unquoted, YAML 1.1 reads it as the boolean `false`. That form
is accepted too, but quoting is clearer.

The list of rules is in [rules.md](rules.md), or run `oc precheck rules`.

## exclusions

An exclusion ignores some objects for some rules. All patterns are shell
globs:

- `*` matches any sequence of characters;
- `?` matches a single character;
- `[a-z]` matches a character class.

```yaml
exclusions:
  - rule: "pods.*"                 # rule ID or pattern; empty means every rule
    namespaces: ["sandbox-*", "*-dev"]
    reason: sandbox environments   # free text, shown by "oc precheck rules"
  - rule: alerts.warning
    names: [KubeCPUOvercommit]     # for alerts, the name is the alertname
  - rule: csr.pending
    names: ["csr-legacy-*"]
```

An object is excluded when the rule matches, and:

- when `namespaces` is set, at least one pattern matches the namespace;
- when `names` is set, at least one pattern matches the name.

Excluded objects are counted in the section ("N item(s) excluded by the
configuration").

The `--pdb-ignore-namespaces` flag is a shortcut for an exclusion on `pdb.*`.

## customRules

Custom rules are written in [CEL](https://cel.dev), the expression language
used by Kubernetes ValidatingAdmissionPolicies.

| Key | Required | Description |
|---|---|---|
| `id` | yes | Lowercase identifier (`a-z0-9 . _ -`), unique, usable in `severities` and `exclusions` |
| `description` | | Shown in the report and in `oc precheck rules` |
| `apiVersion` | yes | `v1`, `apps/v1`, `operators.coreos.com/v1alpha1`… |
| `resource` | yes | Plural resource name: `deployments`, `subscriptions`… |
| `namespaces` | | Restrict the list to these namespaces |
| `labelSelector`, `fieldSelector` | | Server-side filtering |
| `match` | | CEL pre-filter: objects for which it is false are ignored |
| `expr` | yes | CEL condition that must be **true for a compliant object** |
| `aggregate` | | When true, `expr` is evaluated once on the `objects` list |
| `severity` | | `crit`, `warn` (default), `info` or `"off"` |
| `message` | | Static violation message |
| `messageExpression` | | CEL expression returning the message |
| `params` | | Free-form values available as `params.<key>` |

Variables available in expressions:

| Variable | Type | Description |
|---|---|---|
| `object` | map | The object being evaluated (per-object rules) |
| `objects` | list | Every selected object (aggregate rules) |
| `params` | map | The rule's `params` |
| `now` | timestamp | Evaluation time |
| `clusterVersion` | string | Desired OpenShift version |
| `hostedControlPlane` | bool | True on an HCP guest cluster |

Available libraries:

- the CEL standard library;
- the [strings](https://pkg.go.dev/github.com/google/cel-go/ext#Strings),
  [lists](https://pkg.go.dev/github.com/google/cel-go/ext#Lists),
  [sets](https://pkg.go.dev/github.com/google/cel-go/ext#Sets) and
  [math](https://pkg.go.dev/github.com/google/cel-go/ext#Math) extensions.

Tips:

- Use `has(object.spec.field)` before reading an optional field. A missing
  field makes the evaluation fail, and the failure is reported as an `ERROR`
  naming the object.
- Durations: `now - timestamp(object.metadata.creationTimestamp) > duration("1h")`.
- Syntax and type errors are detected when the file is loaded (exit code 3).
- The evaluation cost of each expression is bounded.

### Examples

```yaml
customRules:
  # Per-object rule with parameters
  - id: org.deploy-min-replicas
    description: Application Deployments need at least 2 replicas to survive a drain
    apiVersion: apps/v1
    resource: deployments
    match: '!object.metadata.namespace.startsWith("openshift-")'
    expr: '!has(object.spec.replicas) || object.spec.replicas == 0 || object.spec.replicas >= params.minReplicas'
    messageExpression: '"single replica: " + object.metadata.name'
    params:
      minReplicas: 2

  # Aggregate rule on a label-selected list
  - id: org.no-node-in-maintenance
    apiVersion: v1
    resource: nodes
    labelSelector: example.com/maintenance=true
    aggregate: true
    expr: size(objects) == 0
    messageExpression: '"nodes in maintenance: " + objects.map(n, n.metadata.name).join(", ")'
    severity: crit

  # Time-based rule
  - id: org.pvc-pending
    apiVersion: v1
    resource: persistentvolumeclaims
    expr: 'object.status.phase != "Pending" || now - timestamp(object.metadata.creationTimestamp) < duration("10m")'
    message: PVC Pending for more than 10 minutes
```

More examples are in [../examples/precheck.yaml](../examples/precheck.yaml).
They are exercised by the test suite.
