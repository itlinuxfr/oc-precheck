# Guide: write your own checks

This guide walks you through adding your own checks to oc-upkit, from the
first idea to a scenario that runs them at the right moment. No Go, no
rebuild: a check is a few lines of YAML.

Contents:

1. [How a custom check works](#1-how-a-custom-check-works)
2. [Step 1 — Put the check into words](#step-1--put-the-check-into-words)
3. [Step 2 — Find the resource](#step-2--find-the-resource)
4. [Step 3 — Look at a real object](#step-3--look-at-a-real-object)
5. [Step 4 — Write the condition](#step-4--write-the-condition)
6. [Step 5 — Narrow the objects](#step-5--narrow-the-objects)
7. [Step 6 — Write a useful message](#step-6--write-a-useful-message)
8. [Step 7 — Choose the severity](#step-7--choose-the-severity)
9. [Step 8 — Test the rule](#step-8--test-the-rule)
10. [Step 9 — Put it in a scenario](#step-9--put-it-in-a-scenario)
11. [Rules on a whole list (aggregate)](#rules-on-a-whole-list-aggregate)
12. [CEL cheat sheet](#cel-cheat-sheet)
13. [Pitfalls and error messages](#pitfalls-and-error-messages)
14. [Ready-to-use examples](#ready-to-use-examples)

A complete, tested file with every example of this guide:
[examples/scenario-custom-rules.yaml](../examples/scenario-custom-rules.yaml).

---

## 1. How a custom check works

A custom check is a **rule**. oc-upkit:

1. lists the objects of one kind (`deployments`, `nodes`, Argo CD
   `applications`…), like `oc get` would;
2. evaluates a condition on each object;
3. reports every object for which the condition is **false**.

The condition says what a **compliant** object looks like, not what a bad
one looks like. Read it as "every Deployment *must* have 2 replicas".

The condition is written in [CEL](https://cel.dev), the small expression
language Kubernetes uses in ValidatingAdmissionPolicies and CRD validation.
If you can read `object.spec.replicas >= 2`, you can write CEL.

Rules live in a `customRules` list, either:

- in a **scenario file**: the rules travel with the scenario, which is the
  recommended place for a maintenance routine (step 9);
- in the **configuration file** (`--config`, `UPKIT_CONFIG`): the rules are
  then also run by `oc upkit check`.

The format is the same in both places.

---

## Step 1 — Put the check into words

Write one sentence that starts with "every" or "no":

| Sentence | Kind of rule |
|---|---|
| Every Argo CD Application is Synced and Healthy | per object |
| Every application Deployment has at least 2 replicas | per object |
| No node carries the label `example.com/maintenance=true` | on the whole list |
| An etcd backup succeeded less than 2 hours ago | on the whole list |

"Every X is…" becomes a **per-object** rule. "No X…" or "at least one X…"
becomes a rule on the **whole list** (`aggregate: true`, see
[below](#rules-on-a-whole-list-aggregate)).

We follow the first example, Argo CD Applications, through the next steps.

---

## Step 2 — Find the resource

A rule needs the `apiVersion` and the **plural** resource name, exactly as
the API serves them:

```console
$ oc api-resources | grep -i application
applications    app,apps    argoproj.io/v1alpha1    true    Application
```

The columns are NAME, SHORTNAMES, APIVERSION, NAMESPACED, KIND. Take the
first and the third:

```yaml
apiVersion: argoproj.io/v1alpha1
resource: applications
```

More examples:

| Kind | `apiVersion` | `resource` |
|---|---|---|
| Pod, Node, PVC | `v1` | `pods`, `nodes`, `persistentvolumeclaims` |
| Deployment | `apps/v1` | `deployments` |
| Job, CronJob | `batch/v1` | `jobs`, `cronjobs` |
| OLM Subscription | `operators.coreos.com/v1alpha1` | `subscriptions` |
| MachineConfigPool | `machineconfiguration.openshift.io/v1` | `machineconfigpools` |

If the API is not served on a cluster (Argo CD not installed, for example),
the rule is reported as SKIP on that cluster: it does not fail.

---

## Step 3 — Look at a real object

The condition reads fields of the object. Look at one to know their exact
path:

```console
$ oc -n openshift-gitops get application my-app -o yaml
```

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-app
  namespace: openshift-gitops
spec: …
status:
  health:
    status: Healthy
  sync:
    status: Synced
```

In CEL, the object is the variable `object`, and the YAML path becomes a
dotted path:

| YAML | CEL |
|---|---|
| `metadata.name` | `object.metadata.name` |
| `status.sync.status` | `object.status.sync.status` |
| `metadata.labels["app.kubernetes.io/name"]` | `object.metadata.labels["app.kubernetes.io/name"]` |
| `spec.containers[0].image` | `object.spec.containers[0].image` |

Tip: look at an object in a **bad** state too (a fresh Application, a
failed Job…). It often lacks fields that a healthy one has, and the rule
must cope with that (step 4).

---

## Step 4 — Write the condition

The condition goes in `expr`. It must be **true for a compliant object**.

First attempt:

```yaml
expr: object.status.sync.status == "Synced" && object.status.health.status == "Healthy"
```

It works on healthy Applications, but an Application created a second ago
has **no `status` at all**, and reading a missing field is an evaluation
error. Protect every optional field with `has()`:

```yaml
expr: >-
  has(object.status) &&
  has(object.status.sync) && object.status.sync.status == "Synced" &&
  has(object.status.health) && object.status.health.status == "Healthy"
```

Three things to know:

- **`has()` only tests the last field.** `has(object.status.sync)` fails if
  `object.status` itself is missing; test each level that can be missing,
  from the outside in, as above.
- **`&&` stops at the first false.** That is what makes the chain safe: if
  `has(object.status)` is false, the rest is not evaluated.
- **`>-` lets you write the expression on several lines** without quoting
  problems. Use it as soon as the expression has quotes or is long.

---

## Step 5 — Narrow the objects

By default the rule sees every object of the resource, in every namespace.
Four ways to narrow it, from the cheapest to the most flexible:

```yaml
namespaces: [openshift-gitops]                 # only these namespaces
labelSelector: app.kubernetes.io/part-of=shop  # like oc get -l
fieldSelector: metadata.name=etcd-backup       # like oc get --field-selector
match: '!object.metadata.namespace.startsWith("openshift-")'   # any CEL condition
```

`namespaces`, `labelSelector` and `fieldSelector` filter on the API server.
`match` is a CEL condition evaluated by oc-upkit: objects for which it is
false are simply ignored (not reported). Use it for what selectors cannot
express, such as "every namespace except the platform ones":

```yaml
match: >-
  !object.metadata.namespace.startsWith("openshift-") &&
  !object.metadata.namespace.startsWith("kube-")
```

---

## Step 6 — Write a useful message

Each violation is shown with a message. Three options, by priority:

```yaml
messageExpression: '"sync=" + object.status.sync.status'   # 1. computed (CEL, returns a string)
message: Application not Synced                              # 2. fixed text
description: Argo CD Applications are Synced and Healthy     # 3. used if there is no message
```

The table already shows the namespace and the name of the object; the
message should say **what is wrong** with it. A `messageExpression` must
cope with missing fields too; the conditional operator `cond ? a : b` helps:

```yaml
messageExpression: >-
  !has(object.status) ? "no status yet" :
  "sync=" + (has(object.status.sync) ? object.status.sync.status : "unknown") +
  ", health=" + (has(object.status.health) ? object.status.health.status : "unknown")
```

Numbers must be converted to be concatenated: `string(object.spec.replicas)`.

---

## Step 7 — Choose the severity

```yaml
severity: crit   # crit, warn (default), info, "off"
```

The severity decides what the scenario gate does:

| Severity | In `oc upkit check` | In a scenario |
|---|---|---|
| `crit` | exit code 2 | **stops the scenario** |
| `warn` | reported, exit 0 | asks for confirmation |
| `info` | reported | continues |
| `"off"` | rule disabled | rule disabled |

Write `"off"` with quotes: unquoted, YAML reads `off` as the boolean false.

Keep `crit` for what must really block an upgrade. A gate that stops too
often teaches people to skip it.

Two more keys complete a rule:

```yaml
id: site.argocd-synced          # required: lowercase, a-z 0-9 . _ -, unique
params:                         # optional: values used as params.<key>
  minReplicas: 2
```

`params` keeps thresholds out of the expression
(`object.spec.replicas >= params.minReplicas`), so the next person changes
a number, not CEL. Prefix your IDs with your site or team (`site.`,
`covea.`…) so they never collide with the built-in rules.

The complete rule:

```yaml
customRules:
  - id: site.argocd-synced
    description: Argo CD Applications are Synced and Healthy
    apiVersion: argoproj.io/v1alpha1
    resource: applications
    expr: >-
      has(object.status) &&
      has(object.status.sync) && object.status.sync.status == "Synced" &&
      has(object.status.health) && object.status.health.status == "Healthy"
    messageExpression: >-
      !has(object.status) ? "no status yet" :
      "sync=" + (has(object.status.sync) ? object.status.sync.status : "unknown") +
      ", health=" + (has(object.status.health) ? object.status.health.status : "unknown")
```

---

## Step 8 — Test the rule

Test a rule on its own before putting it in a real scenario. The simplest
is a scenario with a single, read-only step: it asks no confirmation and
changes nothing.

Create `try-rules.yaml`:

```yaml
name: try-rules
customRules:
  - id: site.argocd-synced
    # … the rule from step 7 …
steps:
  - check: {checks: [custom]}
```

Then:

```bash
# 1. Syntax: YAML and CEL errors are reported before connecting to the cluster.
oc upkit run try-rules.yaml --dry-run

# 2. Evaluation on a real cluster (read-only), every object shown.
oc upkit run try-rules.yaml --verbose
```

What to look at in the output:

- the line `site.argocd-synced — …: N violation(s) out of M object(s)`.
  `M` is the number of objects the rule saw: if it is 0, the resource, the
  namespaces or the selectors are wrong;
- the **Violations** table: one row per non-compliant object, with your
  message;
- an `ERROR` line `evaluation failed on N object(s)`: the expression read a
  missing field; the message names the first object concerned (see
  [pitfalls](#pitfalls-and-error-messages)).

Iterate on a lab cluster until the result matches what you expect, ideally
with at least one compliant and one non-compliant object. You can create a
non-compliant object on purpose (scale a test Deployment to 1 replica,
label a node…) to see the rule fire.

Rules in the configuration file are tested the same way, with
`oc upkit config validate --config upkit.yaml` and
`oc upkit check --checks custom --config upkit.yaml --verbose`.

---

## Step 9 — Put it in a scenario

Start from a built-in scenario:

```bash
oc upkit run pre-upgrade --print > my-pre-upgrade.yaml
```

Add a `customRules` section at the top level, next to `name` and `steps`:

```yaml
name: my-pre-upgrade
description: Pre-upgrade with our own checks

customRules:
  - id: site.argocd-synced
    # …
  - id: site.etcd-backup-recent
    # …

steps:
  - check: {}
  # …
```

From then on, **every `check` step that runs the `custom` check evaluates
these rules**, together with those of the configuration file if you pass
one. `check: {}` runs every check, `custom` included.

### Choose which rules run at each step

Some rules only make sense at a given moment. "An etcd backup succeeded
recently" must not block the first gate, since the backup is only taken
later in the scenario. List the rules of a step with `rules`:

```yaml
steps:
  # 1. Gate: every check, every rule except the backup one.
  - check:
      rules: [site.argocd-synced, site.min-replicas, site.no-node-in-maintenance]
  - silence: {duration: 4h}
  - banner: {text: OpenShift upgrade in progress}
  - defrag: {}
  # 5. Take the backup.
  - job: {cronJob: openshift-etcd/etcd-backup, timeout: 1h}
  # 6. Last gate: etcd, pods, and only the backup rule.
  - check:
      checks: [etcd, pods, custom]
      rules: [site.etcd-backup-recent]
```

Rules for `rules`:

- the IDs come from the scenario or from the configuration file;
- an unknown ID is an error, detected before anything runs;
- the step must run the `custom` check: either no `checks` list, or a list
  that contains `custom`.

### Run it

```bash
oc upkit run my-pre-upgrade.yaml --dry-run                          # plan + validation
oc upkit run my-pre-upgrade.yaml --target-version 4.18.24           # for real
oc upkit run my-pre-upgrade.yaml --target-version 4.18.24 -o json > report.json
```

Keep the scenario file in Git, next to your other platform code: a change
of rule goes through a review like any other change.

---

## Rules on a whole list (aggregate)

Some checks are not about one object but about the list: "no node in
maintenance", "at least one recent backup". Set `aggregate: true`: the
expression is evaluated **once**, on the variable `objects` (the list of
selected objects), instead of once per `object`.

"No node carries the maintenance label":

```yaml
- id: site.no-node-in-maintenance
  description: No node carries the maintenance label
  apiVersion: v1
  resource: nodes
  labelSelector: example.com/maintenance=true   # the API returns only these nodes
  aggregate: true
  expr: size(objects) == 0                      # compliant: the list is empty
  messageExpression: '"nodes in maintenance: " + objects.map(n, n.metadata.name).join(", ")'
  severity: crit
```

"A backup Job succeeded less than 2 hours ago":

```yaml
- id: site.etcd-backup-recent
  description: An etcd backup succeeded recently
  apiVersion: batch/v1
  resource: jobs
  namespaces: [openshift-etcd]
  match: object.metadata.name.startsWith(params.prefix)   # match still works per object
  aggregate: true
  expr: >-
    objects.exists(j,
      has(j.status.succeeded) && j.status.succeeded > 0 &&
      has(j.status.completionTime) &&
      now - timestamp(j.status.completionTime) < duration(params.maxAge))
  message: No successful etcd backup in the last 2 hours
  params:
    prefix: etcd-backup
    maxAge: 2h
  severity: crit
```

`match` filters the objects first, one by one; `expr` then sees the
remaining list. An aggregate rule produces at most one violation.

The Jobs created by the scenario `job` step are named
`<cronjob>-upkit-<date>`, so they match the `etcd-backup` prefix like the
scheduled ones.

---

## CEL cheat sheet

| You want | CEL |
|---|---|
| Equal / different | `a == "x"`, `a != "x"` |
| And / or / not | `a && b`, `a \|\| b`, `!a` |
| Field present | `has(object.spec.replicas)` |
| Label or annotation present | `"team" in object.metadata.labels` (after `has(object.metadata.labels)`) |
| Value of a label | `object.metadata.labels["app.kubernetes.io/name"]` |
| Text | `x.startsWith("openshift-")`, `x.endsWith("-prod")`, `x.contains("tmp")`, `x.matches("^v[0-9]+$")` |
| If / else | `cond ? a : b` |
| Size | `size(object.spec.containers)`, `size(objects)` |
| One element matches | `object.spec.containers.exists(c, c.image.endsWith(":latest"))` |
| Every element matches | `object.spec.containers.all(c, has(c.resources.limits))` |
| Exactly one | `list.exists_one(x, …)` |
| Transform a list | `objects.map(o, o.metadata.name)` |
| Filter a list | `objects.filter(o, o.status.phase == "Failed")` |
| Join | `list.join(", ")` |
| Number to text | `string(object.spec.replicas)` |
| Age of an object | `now - timestamp(object.metadata.creationTimestamp) > duration("1h")` |
| Value from params | `params.minReplicas`, `duration(params.maxAge)` |

Variables:

| Variable | Content |
|---|---|
| `object` | the object being evaluated (per-object rules) |
| `objects` | the list of selected objects (aggregate rules) |
| `params` | the `params` of the rule |
| `now` | the current time |
| `clusterVersion` | the OpenShift version of the cluster, e.g. `"4.17.30"` |
| `hostedControlPlane` | true on a HyperShift / HCP guest cluster |

Full reference of keys and libraries: [configuration.md](configuration.md#customrules).

---

## Pitfalls and error messages

**`evaluation failed on N object(s): …: no such key: status`**
The expression read a field that does not exist on that object. Add
`has()` on it **and on each parent level that can be missing**
(`has(object.status) && has(object.status.sync) && …`). The message names
the first object concerned: `oc get` it with `-o yaml` to see what it lacks.

**`0 violation(s) out of 0 object(s)`**
The rule saw nothing. Check `apiVersion` and the plural `resource` with
`oc api-resources`, then `namespaces`, `labelSelector` and `match`.

**`cannot list objects: … forbidden`**
Your account cannot list that resource. The example roles of
[examples/rbac.yaml](../examples/rbac.yaml) only cover the built-in checks:
add `get` and `list` on the resources of your rules.

**`API … not served by this cluster`** (SKIP)
The CRD is not installed on this cluster. That is expected for an operator
installed on some clusters only; it does not fail the check.

**Comparing a number with a text: silently false**
`object.spec.replicas == "2"` is not an error: a number is never equal to a
text, so the condition is always false and every object is reported. Write
numbers without quotes (`== 2`), texts with quotes (`== "Synced"`), and
check in `oc get -o yaml` which one the field is.

**`no such overload`** (evaluation error)
An operation does not exist for the types it received, typically a number
concatenated to a text: `"n=" + object.spec.replicas`. Convert it:
`"n=" + string(object.spec.replicas)`.

**`must return bool`** (when the file is loaded, exit code 3)
`expr` and `match` must be conditions, `messageExpression` a text. The
fields of `object` have no type known in advance, so most type mistakes only
show up at evaluation, on a real object: test every rule (step 8).

**YAML quoting**
An expression that starts with `!`, `"` or contains `: ` must be quoted
(`'…'`) or, simpler, written as a block with `>-`. Inside single quotes,
CEL strings use double quotes: `'object.kind == "Pod"'`.

**`rules: unknown custom rule "…"`**
A step lists an ID that no `customRules` section declares (typo, or the
rule is only in a configuration file you did not pass).

**`rules needs the custom check`**
The step selects rules but does not run the `custom` check: add `custom`
to its `checks`, or remove `custom` from its `skip`.

**`duplicate id "…"`**
The same ID is declared twice, typically in the scenario **and** in the
configuration file. Keep it in one place.

---

## Ready-to-use examples

All of them are in [examples/scenario-custom-rules.yaml](../examples/scenario-custom-rules.yaml)
and run by the test suite.

| ID | Checks | Kind |
|---|---|---|
| `site.argocd-synced` | Argo CD Applications Synced and Healthy | per object |
| `site.min-replicas` | application Deployments with at least 2 replicas | per object, `match`, `params` |
| `site.no-node-in-maintenance` | no node with a maintenance label | aggregate, `labelSelector`, `crit` |
| `site.etcd-backup-recent` | a backup Job succeeded less than 2 h ago | aggregate, time, `params`, `crit` |

More rules (OLM approval mode, PVCs stuck in Pending…) are in
[examples/upkit.yaml](../examples/upkit.yaml).
