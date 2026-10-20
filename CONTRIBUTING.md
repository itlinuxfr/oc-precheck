# Contributing

Thanks for your interest! Issues and pull requests are welcome.

## Development setup

You need Go 1.24 or later. A cluster is not required: the tests use fake
clientsets and a fake pod executor.

```bash
make test         # unit tests
make cover        # race detector + coverage
make lint         # golangci-lint v2 (https://golangci-lint.run)
make docs         # regenerate docs/cli, docs/rules.md and docs/default-config.yaml
make build        # bin/oc-upkit
```

## Project layout

```
cmd/oc-upkit/        main package (thin)
internal/cli/           cobra commands, flags, configuration loading, exit codes
internal/checks/        checks, rule policy, CEL custom rules, operations
internal/report/        result model and text / JSON / YAML rendering
internal/kube/          kubeconfig loading (standard kubectl flags) and pod exec
internal/version/       build information (set with -ldflags)
docs/                   user documentation; docs/cli and docs/rules.md are generated
examples/               example configuration and RBAC
```

## Adding a check

1. Write `func(ctx context.Context, env *Env, o *Options) *report.Section` in
   `internal/checks`. Use the `ruler` (`newRuler`, `r.add`, `r.row`,
   `r.eval`) for every reported problem, so that severities and exclusions
   from the configuration apply.
2. Declare each new rule ID in `Catalog` (`internal/checks/policy.go`). Use the
   format `<check>.<problem>`, in lowercase with dashes.
3. Register the check in `All()` (`internal/checks/env.go`).
4. Add a test with fake clients (see `internal/checks/*_test.go`).
5. Run `make docs` and commit the generated files.

A test fails when a rule is used without being in the catalog, or when the
catalog references an unknown check.

Prefer a [custom CEL rule](docs/configuration.md#customrules) when the need
is specific to one organization.

## Guidelines

- Checks are **read-only**. Anything that changes the cluster is an
  operation:
  - it is never run implicitly;
  - it supports `--dry-run`;
  - it asks for confirmation unless `--yes` is given.
- Never fail the whole run because one API is missing. Return
  `Skipped(...)` when the API is not served, and `Fail(...)` with a hint for
  other errors.
- Messages are in English, short, and actionable.
- Keep dependencies minimal: client-go, cobra, cel-go.

## Commit messages and releases

- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org)
  (`feat:`, `fix:`, `docs:`…). The release changelog is generated from them.
- Releases are cut by pushing a `vX.Y.Z` tag; GoReleaser builds and publishes
  the archives.

By contributing, you agree that your contributions are licensed under the
Apache License 2.0.
