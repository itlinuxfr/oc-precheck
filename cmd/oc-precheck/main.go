// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

// Command oc-precheck runs pre-upgrade checks and maintenance operations
// against an OpenShift 4 cluster. Installed as "oc-precheck" (or
// "kubectl-precheck") in the PATH, it is available as "oc precheck".
package main

import (
	"os"

	"github.com/itlinuxfr/oc-precheck/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
