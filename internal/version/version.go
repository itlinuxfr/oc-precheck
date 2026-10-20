// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

// Package version exposes build information injected at link time:
//
//	go build -ldflags "-X github.com/itlinuxfr/oc-upkit/internal/version.Version=v1.0.0 ..."
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set through -ldflags -X at build time (see Makefile and .goreleaser.yaml).
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Info is the build information.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get returns the build information, falling back to the VCS data embedded
// by the Go toolchain when ldflags were not set (go install).
func Get() Info {
	i := Info{Version: Version, Commit: Commit, Date: Date, GoVersion: runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if i.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			i.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if i.Commit == "" {
					i.Commit = s.Value
				}
			case "vcs.time":
				if i.Date == "" {
					i.Date = s.Value
				}
			}
		}
	}
	return i
}

func (i Info) String() string {
	c := i.Commit
	if len(c) > 12 {
		c = c[:12]
	}
	return fmt.Sprintf("oc-upkit %s (commit %s, built %s, %s, %s)", i.Version, orUnknown(c), orUnknown(i.Date), i.GoVersion, i.Platform)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
