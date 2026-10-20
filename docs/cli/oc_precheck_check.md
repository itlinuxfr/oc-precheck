## oc precheck check

Run the pre-upgrade checks (default command)

### Synopsis

Run the pre-upgrade checks. All checks are read-only.

The optional actions (--silence, --banner, --remove-banner, --defrag) only run
when requested, after confirmation (or --yes), and are refused when a check is
CRITICAL unless --force is given.

Exit codes: 0 OK, 1 warnings (with --fail-on warn), 2 critical, 3 usage or
configuration error, 4 execution error.

```
oc precheck check [flags]
```

### Examples

```
  oc precheck
  oc precheck check --checks etcd,mcp,pdb --verbose
  oc precheck --config precheck.yaml --target-version 4.18.24
  oc precheck --group-by-label topology.kubernetes.io/zone -o json > precheck.json
  oc precheck --silence --banner "Upgrade to 4.18 in progress" --defrag --dry-run
```

### Options

```
      --alert-severities strings          Alert severities to report (default [critical,warning])
      --alertmanager-url string           Alertmanager URL as seen from its pod (default "http://localhost:9093")
      --banner string                     ACTION: show this text as a web console banner
      --banner-bg string                  Banner background color (default "#c9190b")
      --banner-color string               Banner text color (default "#fff")
      --banner-link string                Optional link (e.g. change ticket)
      --banner-link-text string           Link text
      --banner-location string            BannerTop, BannerBottom or BannerTopBottom (default "BannerTop")
      --banner-name string                ConsoleNotification name (default "oc-precheck-maintenance")
      --checks strings                    Checks to run (default: all), see --list-checks
      --defrag                            ACTION: defragment etcd, leader last
      --defrag-command-timeout duration   Timeout of etcdctl defrag (default 1m0s)
      --defrag-disarm-alarms              Disarm etcd alarms (NOSPACE) after defragmentation
      --defrag-min-frag float             Only defragment members above this fragmentation (%)
      --defrag-pause duration             Pause between two members (default 15s)
      --dry-run                           Show what would be done without changing anything
      --etcd-db-size-warn string          etcd database size above which to warn (default "6Gi")
      --etcd-expected-members int         Expected number of etcd members (default 3)
      --etcd-frag-threshold float         etcd fragmentation (%) above which a defragmentation is recommended (default 45)
      --etcd-namespace string             Namespace of the etcd pods (default "openshift-etcd")
      --fail-on string                    Level giving a non-zero exit code: warn or crit (default "crit")
      --force                             Run the actions even when checks are CRITICAL
      --group-by-label strings            Node labels used to group the pod count (repeatable)
      --hcp-filter strings                Restrict to HostedClusters (globs on name or namespace/name)
  -h, --help                              help for check
      --ignore-alerts strings             Alerts to ignore (alertname) (default [Watchdog,AlertmanagerReceiversNotConfigured,InfoInhibitor])
      --job-long-duration duration        Warn about Jobs running longer than this (default 1h0m0s)
      --list-checks                       List the available checks and exit
      --monitoring-namespace string       Namespace of the platform monitoring stack (default "openshift-monitoring")
      --node-pod-capacity float           Pod capacity usage (%) per node above which to warn (default 90)
      --node-selector string              Restrict the nodes check to a label selector
      --pdb-ignore-namespaces strings     Namespaces whose PDBs are ignored (exclusion shortcut)
      --pod-notready-age duration         Report Running pods not ready for longer than this (0 = immediately)
      --pod-pending-age duration          Report Pending pods older than this (default 5m0s)
      --pod-restart-warn int32            Restart count reported as information (default 10)
      --pod-terminating-age duration      Report pods Terminating for longer than this (default 10m0s)
      --quota-threshold float             Quota usage (%) above which to warn (default 90)
      --remove-banner                     ACTION: remove the banners created by oc-precheck (end of maintenance)
      --silence                           ACTION: create an Alertmanager silence for the maintenance window
      --silence-author string             Silence author (default: current OS user)
      --silence-comment string            Silence comment (default "OpenShift upgrade in progress (oc-precheck)")
      --silence-duration duration         Silence duration (default 3h0m0s)
      --silence-matcher stringArray       amtool matcher (repeatable), default alertname=~.+ (everything)
      --skip strings                      Checks to skip
      --target-version string             Target version (e.g. 4.18.24), validated against the update graph
  -y, --yes                               Do not ask for confirmation
      --zone-label string                 Node label holding the zone / room (default "topology.kubernetes.io/zone")
```

### Options inherited from parent commands

```
      --as string                      Username to impersonate for the operation
      --as-group stringArray           Group to impersonate for the operation, this flag can be repeated to specify multiple groups.
      --as-uid string                  UID to impersonate for the operation
      --certificate-authority string   Path to a cert file for the certificate authority
      --client-certificate string      Path to a client certificate file for TLS
      --client-key string              Path to a client key file for TLS
      --cluster string                 The name of the kubeconfig cluster to use
      --config string                  Configuration file (rules, thresholds, exclusions); defaults to $OCP_PRECHECK_CONFIG
      --context string                 The name of the kubeconfig context to use
      --disable-compression            If true, opt-out of response compression for all requests to the server
      --insecure-skip-tls-verify       If true, the server's certificate will not be checked for validity. This will make your HTTPS connections insecure
      --kubeconfig string              Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --max-rows int                   Maximum rows per table (0 = unlimited) (default 50)
      --no-color                       Disable colors (also honors NO_COLOR)
  -o, --output string                  Output format: text, json or yaml (default "text")
      --password string                Password for basic authentication to the API server
      --proxy-url string               If provided, this URL will be used to connect via proxy
      --request-timeout string         The length of time to wait before giving up on a single server request. Non-zero values should contain a corresponding time unit (e.g. 1s, 2m, 3h). A value of zero means don't timeout requests. (default "0")
      --server string                  The address and port of the Kubernetes API server
      --timeout duration               Global timeout of the command (default 2h0m0s)
      --tls-server-name string         If provided, this name will be used to validate server certificate. If this is not provided, hostname used to contact the server is used.
      --token string                   Bearer token for authentication to the API server
      --user string                    The name of the kubeconfig user to use
      --username string                Username for basic authentication to the API server
      --verbose                        Also show healthy items
```

### SEE ALSO

* [oc precheck](oc_precheck.md)	 - Pre-upgrade checks and maintenance operations for OpenShift 4

