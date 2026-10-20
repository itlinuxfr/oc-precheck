# Scenarios

A scenario is an ordered list of steps: read-only checks and maintenance
actions of oc-upkit. It turns the usual "before / after the upgrade" routine
into one command that always runs the same way.

```bash
oc upkit run                       # list the built-in scenarios
oc upkit run pre-upgrade --dry-run # show the plan, run nothing
oc upkit run pre-upgrade           # run it (confirmation first)
```

Two principles:

- **A scenario only uses the operations of the tool.** There is no shell
  step: what a scenario can do is what `oc upkit` can do, tested and
  documented.
- **oc-upkit never starts the upgrade.** `pre-upgrade` prepares the cluster;
  running `oc adm upgrade` stays the operator's decision.

## How a run works

1. The scenario is loaded and validated (unknown keys, unknown checks,
   missing fields are reported before anything runs).
2. The plan is shown, each step marked *read-only* or *changes the cluster*.
3. If at least one step changes the cluster, you confirm once (`--yes` to
   skip the question).
4. The steps run in order. After each step, a **gate** decides what happens:

   | Step result | Gate |
   |---|---|
   | PASS, INFO, SKIP | continue |
   | WARN | ask for confirmation (continue with `--yes`; never asked after the last step) |
   | CRIT | stop |
   | ERROR (the step could not run, or an action failed) | stop |

   When a scenario stops, the changes of the steps already run stay in place
   (for example the silence and the banner); the summary says so.
5. The summary lists every step with its status; the steps after a stop are
   shown as *not run*.

`--yes` answers the confirmations, it never overrides a critical step.
Without a terminal (CI, cron), questions cannot be asked: pass `--yes`, or
the scenario stops at the first confirmation.

## Built-in scenarios

`pre-upgrade`:

```yaml
steps:
  - check: {}                     # gate: nothing changes if a check is critical
  - silence: {duration: 4h, comment: OpenShift upgrade in progress (oc-upkit)}
  - banner: {text: OpenShift upgrade in progress}
  - defrag: {}
  - check: {checks: [etcd, pods]}
```

`post-upgrade`:

```yaml
steps:
  - check: {}                     # gate: banner and silences stay while something is critical
  - removeBanner: {}
  - expireSilences: {}
```

Print the full files, comments included, with `--print`.

## Writing your own

```bash
oc upkit run pre-upgrade --print > my-pre-upgrade.yaml
# edit the file
oc upkit run my-pre-upgrade.yaml --dry-run
oc upkit run my-pre-upgrade.yaml
```

A file has a `name`, an optional `description` and a list of `steps`. Each
step holds exactly one operation:

| Step | Fields (all optional unless noted) | Changes the cluster |
|---|---|---|
| `check` | `checks` (list, default: all), `skip` (list), `rules` (custom rule IDs evaluated at this step, default: all; the step must run the `custom` check). With neither `checks` nor `skip`, `settings.checks` / `settings.skip` of the configuration file apply. | no |
| `silence` | `duration` (default `4h`), `comment`, `author` (default: current user), `matchers` (amtool syntax, default: every alert). Skipped when a silence created by oc-upkit muting every alert is already active. | yes |
| `banner` | `text` (**required**), `name`, `location` (`BannerTop`, `BannerBottom`, `BannerTopBottom`), `color`, `background`, `link`, `linkText` | yes |
| `defrag` | `minFragmentation` (%), `pause` (default `15s`), `commandTimeout` (default `60s`), `disarmAlarms` | yes |
| `job` | `cronJob` (**required**, `namespace/name`), `timeout` (default `1h`). Creates a Job from the CronJob template (like `oc create job --from=cronjob/…`) and waits for it to succeed. | yes |
| `removeBanner` | `names` (default: every banner created by oc-upkit) | yes |
| `expireSilences` | — Expires the active silences created by oc-upkit (or oc-precheck). | yes |

Durations use the Go syntax: `90s`, `15m`, `4h`.

Example with a site-specific etcd backup between the defragmentation and the
last checks:

```yaml
name: my-pre-upgrade
description: Pre-upgrade with our etcd backup
steps:
  - check: {}
  - silence: {duration: 4h, comment: "Upgrade to 4.18 — CHG0012345"}
  - banner:
      text: Upgrade to 4.18 in progress
      link: https://itsm.example.com/CHG0012345
      linkText: CHG0012345
  - defrag: {}
  - job: {cronJob: openshift-etcd/etcd-backup, timeout: 1h}
  - check: {checks: [etcd, pods]}
```

## Your own checks in a scenario

A scenario can carry CEL rules in a `customRules` section, in the format of
the configuration file. They are evaluated by the `custom` check of the
`check` steps, together with the rules of the configuration file; `rules`
selects some of them for one step:

```yaml
name: my-pre-upgrade
customRules:
  - id: site.argocd-synced
    apiVersion: argoproj.io/v1alpha1
    resource: applications
    expr: >-
      has(object.status) && has(object.status.sync) &&
      object.status.sync.status == "Synced"
steps:
  - check: {}                                            # every check and rule
  - check: {checks: [custom], rules: [site.argocd-synced]}  # only this rule
```

Step-by-step guide, with tested examples:
[guide-custom-checks.md](guide-custom-checks.md).

## Target version

`oc upkit run pre-upgrade --target-version 4.18.24` validates the update
path in the `clusterversion` check of the scenario, like
`oc upkit check --target-version`. It can also be set in the configuration
file (`settings.targetVersion`).

## Configuration file

The configuration file (`--config` / `UPKIT_CONFIG`) applies to the check
steps as it does to `oc upkit check`: thresholds, severities, exclusions and
custom rules.

## Report and exit codes

The text output shows each step as it runs, then a summary. With `-o json`
(or `-o yaml`), the report has the usual `sections` plus a `scenario` object:

```json
{
  "scenario": {
    "name": "pre-upgrade",
    "source": "built-in",
    "user": "ben",
    "tool": "oc-upkit v0.4.0",
    "startedAt": "2026-10-20T21:30:00+02:00",
    "finishedAt": "2026-10-20T21:42:13+02:00",
    "outcome": "completed",
    "steps": [
      {"index": 1, "kind": "check", "description": "Run all checks",
       "changesCluster": false, "status": "WARN", "ran": true, "sections": [0, 1, 2]}
    ]
  },
  "sections": ["…"]
}
```

`sections` of a step are indexes in the top-level `sections` list.
Confirmation questions are written to standard error, so the JSON on
standard output stays valid.

| `outcome` | Meaning | Exit code |
|---|---|---|
| `completed` | every step ran | 0 |
| `planned` | `--dry-run`, nothing ran | 0 |
| `declined` | you answered no (plan or warning gate) | 1 |
| `stopped` | a step was critical / a step failed | 2 / 4 |
| `canceled` | interrupted (Ctrl-C, `--timeout`) | 4 |

## Permissions

The `job` step needs `create` on `jobs` (and `get` on `cronjobs`) in the
namespace of the CronJob; the other steps need the same rights as the
corresponding commands. See [examples/rbac.yaml](../examples/rbac.yaml).
