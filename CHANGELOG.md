# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org).

## [Unreleased]

### Changed
- **Renamed from `oc-precheck` to `oc-upkit`**: the tool now covers the whole
  upgrade (checks and maintenance operations), not only pre-checks. Install the
  binary as `oc-upkit` / `kubectl-upkit` and run `oc upkit`. Module path:
  `github.com/itlinuxfr/oc-upkit`.
- Configuration: `UPKIT_CONFIG` replaces `OCP_PRECHECK_CONFIG`, and the example
  file is now `upkit.yaml`.
- Web console banners and Alertmanager silences are now marked `oc-upkit`.

### Deprecated
- `OCP_PRECHECK_CONFIG` is still read, with a warning, and will be removed in
  a future release.

### Fixed
- `banner remove --all` also removes the banners created by `oc-precheck`
  before the rename.

## [0.3.0] - 2026-10-19

### Added
- `oc` / `kubectl` plugin mode: install the binary as `oc-precheck` or
  `kubectl-precheck`; help and examples then show `oc precheck`.
- Standard kubectl connection flags (`--context`, `--server`, `--token`,
  `--as`, `--certificate-authority`, `--request-timeout`…).
- `config default`: print the default configuration, generated from the code.
- `config validate`.
- `banner remove --all` and `check --remove-banner`.
- YAML output (`-o yaml`), `version` command with build information, and
  shell completion.
- Generated reference documentation (`docs/cli`, `docs/rules.md`).
- Example RBAC.

### Changed
- All messages, help and documentation are now in English.
- The CLI is built with cobra.
- Module path: `github.com/itlinuxfr/oc-precheck`.

## [0.2.0] - 2026-10-19

### Added
- Rule policy: 84 rule IDs with configurable severities and exclusions.
- YAML configuration file.
- Custom rules in CEL.
- Checks: `silences`, `jobs`, `quotas`, `hostedclusters`; `pods` shows
  `READY x/y` and more error states.
- Operations: `watch`, `drain`/`uncordon` by label with a pre-flight analysis,
  console banner, silence management.

## [0.1.0] - 2026-10-19

### Added
- First version: clusterversion, alerts, etcd, clusteroperators, olm, mcp,
  pdb, nodes, pods and csr checks; Alertmanager silence and etcd
  defragmentation actions.
