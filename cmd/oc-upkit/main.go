// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

// Command oc-upkit runs pre-upgrade checks and maintenance operations
// against an OpenShift 4 cluster. Installed as "oc-upkit" (or
// "kubectl-upkit") in the PATH, it is available as "oc upkit".
package main

import (
	"os"

	"github.com/itlinuxfr/oc-upkit/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
