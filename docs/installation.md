# Installation

## Release binaries

Each [GitHub release](https://github.com/itlinuxfr/oc-upkit/releases) ships:

- static binaries (`CGO_ENABLED=0`) for linux, darwin and windows, on amd64
  and arm64;
- a `checksums.txt` file.

The Linux binary has no dependency and runs on RHEL 8 and 9.

```bash
VERSION=v0.3.0 OS=linux ARCH=amd64
BASE=https://github.com/itlinuxfr/oc-upkit/releases/download/${VERSION}
curl -sSLO "${BASE}/oc-upkit_${VERSION#v}_${OS}_${ARCH}.tar.gz"
curl -sSLO "${BASE}/checksums.txt"
sha256sum --ignore-missing -c checksums.txt
tar xzf "oc-upkit_${VERSION#v}_${OS}_${ARCH}.tar.gz" oc-upkit
```

## As an `oc` / `kubectl` plugin

`oc` and `kubectl` turn any executable of the `PATH` named `oc-<name>` or
`kubectl-<name>` into a sub-command.

```bash
install -m 0755 oc-upkit /usr/local/bin/oc-upkit
ln -sf /usr/local/bin/oc-upkit /usr/local/bin/kubectl-upkit   # optional
oc plugin list
oc upkit --help
```

When run as a plugin, the help and examples show `oc upkit`. The connection
flags (`--context`, `--server`, `--token`, `--as`…) are handled by the
plugin itself, so `oc upkit --context prod` behaves like a native command.

On Windows, copy `oc-upkit.exe` to a folder of the `PATH` (for example the
folder that holds `oc.exe`).

## Air-gapped environments

The binary only talks to the Kubernetes API server of the cluster. It needs no
internet access, no container image and no in-cluster component. Copy the
archive and the checksums file, verify them, and install as above.

## With Go

```bash
go install github.com/itlinuxfr/oc-upkit/cmd/oc-upkit@latest
```

## From source

```bash
git clone https://github.com/itlinuxfr/oc-upkit
cd oc-upkit
make build            # bin/oc-upkit
make plugin-install   # ~/.local/bin/oc-upkit and kubectl-upkit
make cross            # dist/ for every platform
```

It requires Go 1.24 or later. Behind a corporate proxy, point `GOPROXY` to your
internal Go module proxy.

## Shell completion

```bash
oc-upkit completion bash > /etc/bash_completion.d/oc-upkit
oc-upkit completion zsh  > "${fpath[1]}/_oc-upkit"
```
