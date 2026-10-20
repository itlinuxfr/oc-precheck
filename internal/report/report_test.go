// Copyright 2026 The oc-upkit Authors.
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSectionStatus(t *testing.T) {
	s := NewSection("x", "X")
	s.Add(Info, "info does not escalate")
	if s.Status != Pass {
		t.Fatalf("status = %v", s.Status)
	}
	s.Add(Warn, "w")
	s.AddRule("r", Crit, "c")
	if s.Status != Crit {
		t.Fatalf("status = %v", s.Status)
	}
	s.Fail(errors.New("boom"), "hint")
	if s.Status != Error || !strings.Contains(s.Findings[len(s.Findings)-1].Message, "boom — hint") {
		t.Fatalf("fail: %+v", s)
	}
}

func TestTableLimitAndRender(t *testing.T) {
	s := NewSection("x", "Title")
	tb := s.NewTable("T", "A", "B")
	for i := 0; i < 5; i++ {
		tb.AddRow(Warn, "a", strings.Repeat("é", 100))
	}
	tb.Limit(2)
	if len(tb.Rows) != 2 || tb.Truncated != 3 {
		t.Fatalf("limit: %d rows, %d truncated", len(tb.Rows), tb.Truncated)
	}
	var b bytes.Buffer
	(&Printer{W: &b}).Section(s)
	out := b.String()
	if !strings.Contains(out, "… 3 more row(s) hidden") || !strings.Contains(out, "…") || strings.Contains(out, "\033[") {
		t.Fatalf("render:\n%s", out)
	}
	b.Reset()
	(&Printer{W: &b, Color: true}).Section(s)
	if !strings.Contains(b.String(), "\033[") {
		t.Fatal("expected ANSI colors")
	}
}

func TestReportEncoding(t *testing.T) {
	r := &Report{Sections: []*Section{NewSection("a", "A")}}
	r.Sections[0].Add(Warn, "w")
	r.Compute()
	var j, y bytes.Buffer
	if err := JSON(&j, r); err != nil {
		t.Fatal(err)
	}
	if err := YAML(&y, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(j.String(), `"status": "WARN"`) || !strings.Contains(y.String(), "status: WARN") {
		t.Fatalf("json:\n%s\nyaml:\n%s", j.String(), y.String())
	}
}
