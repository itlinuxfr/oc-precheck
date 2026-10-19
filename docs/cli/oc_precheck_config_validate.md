## oc precheck config validate

Validate a configuration file (exit code 3 when invalid)

```
oc precheck config validate [flags]
```

### Examples

```
  oc precheck config validate --config precheck.yaml
```

### Options

```
  -h, --help   help for validate
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
      --no-color                       Disable colors (also honours NO_COLOR)
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

* [oc precheck config](oc_precheck_config.md)	 - Generate or validate a configuration file

