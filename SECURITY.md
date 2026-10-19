# Security policy

## Reporting a vulnerability

Please do **not** open a public issue. Report vulnerabilities privately through
[GitHub security advisories](https://github.com/itlinuxfr/oc-precheck/security/advisories/new).
You should get an answer within a week.

## Scope and design notes

- oc-precheck runs with the permissions of the kubeconfig in use. It does not
  store or transmit credentials anywhere else than to the API server.
- The checks are read-only. Operations that change the cluster (silences,
  banners, defragmentation, cordon/drain) are explicit and ask for
  confirmation.
- `pods/exec` is used to run `amtool` in the Alertmanager pod and `etcdctl` in
  the etcd pods. The commands are fixed and never built from user input,
  except silence matchers and comments, which are passed as separate
  arguments: no shell is involved.
- Custom CEL rules are evaluated locally, with a bounded cost.
