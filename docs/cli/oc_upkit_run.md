## oc upkit run

Run a scenario: checks and maintenance actions, step by step

### Synopsis

Run a scenario: an ordered list of checks and maintenance actions with a
gate after each step.

  - a step ending CRITICAL (or failing) stops the scenario;
  - a step ending with warnings asks for confirmation (--yes to continue);
  - the plan is shown first, and nothing runs before you confirm it.

Built-in scenarios: pre-upgrade and post-upgrade. To adapt one, export it
with --print, edit the file, and run the file. Without argument, the built-in
scenarios are listed.

oc-upkit never starts the upgrade itself: run "oc adm upgrade" once the
pre-upgrade scenario is completed.

Exit codes: 0 completed, 1 stopped at your request, 2 stopped on a critical
step, 3 usage or scenario error, 4 a step failed or the run was interrupted.

```
oc upkit run [SCENARIO | FILE] [flags]
```

### Examples

```
  oc upkit run
  oc upkit run pre-upgrade --dry-run
  oc upkit run pre-upgrade
  oc upkit run pre-upgrade --print > my-pre-upgrade.yaml
  oc upkit run my-pre-upgrade.yaml -o json > pre-upgrade-report.json
  oc upkit run post-upgrade --yes
```

### Options

```
      --dry-run   Show the plan without running anything
  -h, --help      help for run
      --print     Print the scenario as YAML and exit (to customize a built-in one)
  -y, --yes       Do not ask for confirmation (critical steps still stop the scenario)
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
      --config string                  Configuration file (rules, thresholds, exclusions); defaults to $UPKIT_CONFIG
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

* [oc upkit](oc_upkit.md)	 - Upgrade toolkit for OpenShift 4: pre-upgrade checks and maintenance operations

