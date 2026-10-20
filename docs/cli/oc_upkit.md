## oc upkit

Upgrade toolkit for OpenShift 4: pre-upgrade checks and maintenance operations

### Synopsis

oc-upkit verifies that an OpenShift 4 cluster is ready to be upgraded and
helps run the maintenance around it: Alertmanager silences, web console banner,
etcd defragmentation, drain of a whole room / zone, live node view, and a global
status of HostedClusters from a HyperShift hub.

Running it without a command runs "check". It works as an oc / kubectl plugin
when installed as "oc-upkit" or "kubectl-upkit" in the PATH.

### Options

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
  -h, --help                           help for oc upkit
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

* [oc upkit banner](oc_upkit_banner.md)	 - Manage the web console banner (ConsoleNotification)
* [oc upkit check](oc_upkit_check.md)	 - Run the pre-upgrade checks (default command)
* [oc upkit completion](oc_upkit_completion.md)	 - Generate the autocompletion script for the specified shell
* [oc upkit config](oc_upkit_config.md)	 - Generate or validate a configuration file
* [oc upkit defrag](oc_upkit_defrag.md)	 - Defragment etcd member by member, leader last
* [oc upkit drain](oc_upkit_drain.md)	 - Drain every node matching a label selector (e.g. a whole room)
* [oc upkit rules](oc_upkit_rules.md)	 - List the rules with their default and effective severity
* [oc upkit silence](oc_upkit_silence.md)	 - Manage Alertmanager silences (shown with their UUID)
* [oc upkit uncordon](oc_upkit_uncordon.md)	 - Make the nodes matching a label selector schedulable again
* [oc upkit version](oc_upkit_version.md)	 - Print the version
* [oc upkit watch](oc_upkit_watch.md)	 - Live view of nodes, MachineConfigPools and HostedClusters

