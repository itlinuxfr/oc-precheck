// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCommand(IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut}, "oc-upkit")
	root.SetArgs(defaultToCheck(root, args))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func exitCode(err error) int {
	if err == nil {
		return CodeOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return CodeUsage
}

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/oc-upkit":      "oc upkit",
		"kubectl-upkit":                "kubectl upkit",
		`C:\tools\oc-upkit.exe`:        "oc upkit",
		"/opt/bin/oc-upgrade_precheck": "oc upgrade-precheck",
		"./my-build":                   "my-build",
	}
	for in, want := range cases {
		if got := commandName(in); got != want {
			t.Errorf("commandName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultToCheck(t *testing.T) {
	root := NewRootCommand(IOStreams{Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}, "oc-upkit")
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "check"},
		{[]string{"--target-version", "4.18.24"}, "check --target-version 4.18.24"},
		{[]string{"--context", "prod", "drain", "-l", "a=b"}, "--context prod drain -l a=b"},
		{[]string{"watch"}, "watch"},
		{[]string{"--help"}, "--help"},
		{[]string{"completion", "bash"}, "completion bash"},
	}
	for _, c := range cases {
		if got := strings.Join(defaultToCheck(root, c.in), " "); got != c.want {
			t.Errorf("defaultToCheck(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestConfigDefaultAndValidate(t *testing.T) {
	out, _, err := run(t, "config", "default")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "severities:") || !strings.Contains(out, "etcd.fragmentation:") {
		t.Fatalf("unexpected default config:\n%s", out)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "upkit.yaml")
	if _, _, err := run(t, "config", "default", "--output-file", path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "config", "default", "--output-file", path); exitCode(err) != CodeUsage {
		t.Fatalf("existing file must not be overwritten, got %v", err)
	}
	out, _, err = run(t, "config", "validate", "--config", path)
	if err != nil || !strings.Contains(out, "is valid") {
		t.Fatalf("validate: %v %q", err, out)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("severities:\n  pdb.blocked: crit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "config", "validate", "--config", bad); exitCode(err) != CodeUsage || !strings.Contains(err.Error(), "pdb.blocked") {
		t.Fatalf("invalid config must exit 3, got %v", err)
	}
	if _, _, err := run(t, "config", "validate", "--config", "../../examples/upkit.yaml"); err != nil {
		t.Fatalf("example config: %v", err)
	}
}

func TestRulesCommand(t *testing.T) {
	out, _, err := run(t, "rules", "-o", "json", "--config", "../../examples/upkit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var rows []ruleRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	eff := map[string]string{}
	for _, r := range rows {
		eff[r.ID] = r.Effective
	}
	if eff["mcp.paused"] != "info" || eff["pods.failed"] != "off" || eff["org.olm-manual-approval"] != "warn" || eff["etcd.alarm"] != "crit" {
		t.Fatalf("effective severities: %v", eff)
	}
	md, _, _ := run(t, "rules", "--markdown", "--check", "etcd")
	if !strings.HasPrefix(md, "| Rule |") || !strings.Contains(md, "`etcd.no-leader`") || strings.Contains(md, "pods.") {
		t.Fatalf("markdown: %s", md)
	}
}

func TestUsageErrors(t *testing.T) {
	if _, _, err := run(t, "--checks", "nope"); exitCode(err) != CodeUsage {
		t.Errorf("unknown check: %v", err)
	}
	if _, _, err := run(t, "-o", "xml", "rules"); exitCode(err) != CodeUsage {
		t.Errorf("bad output: %v", err)
	}
	if _, _, err := run(t, "drain"); exitCode(err) != CodeUsage {
		t.Errorf("drain without selector: %v", err)
	}
	if out, _, err := run(t, "--list-checks"); err != nil || !strings.Contains(out, "hostedclusters") {
		t.Errorf("list-checks: %v %q", err, out)
	}
}

func TestPluginHelpUsesDisplayName(t *testing.T) {
	var out bytes.Buffer
	root := NewRootCommand(IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &out}, "/usr/bin/oc-upkit")
	root.SetArgs([]string{"drain", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "oc upkit drain") {
		t.Fatalf("help should show the plugin name:\n%s", out.String())
	}
}

func TestRunListAndPrint(t *testing.T) {
	out, _, err := run(t, "run")
	if err != nil || !strings.Contains(out, "pre-upgrade") || !strings.Contains(out, "post-upgrade") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, _, err = run(t, "run", "pre-upgrade", "--print")
	if err != nil || !strings.HasPrefix(out, "# Prepare an OpenShift cluster") || !strings.Contains(out, "- defrag: {}") {
		t.Fatalf("print: %v\n%s", err, out)
	}
	// The printed scenario can be saved and run as a file.
	p := filepath.Join(t.TempDir(), "mine.yaml")
	if err := os.WriteFile(p, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	if again, _, err := run(t, "run", p, "--print"); err != nil || again != out {
		t.Fatalf("print file: %v", err)
	}
}

func TestRunInvalidScenario(t *testing.T) {
	if _, _, err := run(t, "run", "nope"); exitCode(err) != CodeUsage || !strings.Contains(err.Error(), "neither a built-in") {
		t.Fatalf("unknown scenario: %v", err)
	}
	p := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(p, []byte("name: bad\nsteps:\n- check: {checks: [etcdd]}\n- banner: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "run", p)
	if exitCode(err) != CodeUsage || !strings.Contains(err.Error(), `unknown check "etcdd"`) || !strings.Contains(err.Error(), "banner text") {
		t.Fatalf("invalid scenario: %v", err)
	}
}
