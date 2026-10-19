// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/itlinuxfr/oc-precheck/internal/report"
)

// CustomRuleSpec is a rule declared in the configuration file.
//
// Expr is a CEL expression that must evaluate to TRUE for a compliant
// object (same convention as ValidatingAdmissionPolicy validations).
// Variables: object (per-object rules), objects (aggregate rules), params,
// now (timestamp), clusterVersion (string), hostedControlPlane (bool).
type CustomRuleSpec struct {
	ID                string         `json:"id"`
	Description       string         `json:"description,omitempty"`
	APIVersion        string         `json:"apiVersion"`
	Resource          string         `json:"resource"`
	Namespaces        []string       `json:"namespaces,omitempty"`
	LabelSelector     string         `json:"labelSelector,omitempty"`
	FieldSelector     string         `json:"fieldSelector,omitempty"`
	Match             string         `json:"match,omitempty"`
	Expr              string         `json:"expr"`
	Aggregate         bool           `json:"aggregate,omitempty"`
	Severity          string         `json:"severity,omitempty"`
	Message           string         `json:"message,omitempty"`
	MessageExpression string         `json:"messageExpression,omitempty"`
	Params            map[string]any `json:"params,omitempty"`
}

// CustomRule is a compiled custom rule.
type CustomRule struct {
	CustomRuleSpec
	GVR   schema.GroupVersionResource
	Level report.Level
	match cel.Program
	expr  cel.Program
	msg   cel.Program
}

var ruleIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// celCostLimit bounds the evaluation cost of a single expression.
const celCostLimit = 10_000_000

func celEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("object", cel.DynType),
		cel.Variable("objects", cel.ListType(cel.DynType)),
		cel.Variable("params", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("now", cel.TimestampType),
		cel.Variable("clusterVersion", cel.StringType),
		cel.Variable("hostedControlPlane", cel.BoolType),
		ext.Strings(), ext.Sets(), ext.Math(), ext.Lists(),
	)
}

func compile(env *cel.Env, src string, want *cel.Type) (cel.Program, error) {
	ast, iss := env.Compile(src)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	if ot := ast.OutputType(); ot != cel.DynType && !ot.IsExactType(want) {
		return nil, fmt.Errorf("must return %s, returns %s", want, ot)
	}
	return env.Program(ast, cel.CostLimit(celCostLimit), cel.InterruptCheckFrequency(100))
}

// CompileCustomRules validates and compiles the rules of the configuration.
func CompileCustomRules(specs []CustomRuleSpec) ([]*CustomRule, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	env, err := celEnv()
	if err != nil {
		return nil, err
	}
	var out []*CustomRule
	for i, s := range specs {
		where := fmt.Sprintf("customRules[%d]", i)
		if s.ID != "" {
			where += " (" + s.ID + ")"
		}
		if !ruleIDRe.MatchString(s.ID) {
			return nil, fmt.Errorf("%s: id is required, lowercase (a-z0-9 . _ -)", where)
		}
		if s.APIVersion == "" || s.Resource == "" {
			return nil, fmt.Errorf("%s: apiVersion and resource (plural, e.g. deployments) are required", where)
		}
		if s.Expr == "" {
			return nil, fmt.Errorf("%s: expr is required", where)
		}
		gv, err := schema.ParseGroupVersion(s.APIVersion)
		if err != nil {
			return nil, fmt.Errorf("%s: apiVersion: %w", where, err)
		}
		r := &CustomRule{CustomRuleSpec: s, GVR: gv.WithResource(strings.ToLower(s.Resource)), Level: report.Warn}
		if s.Severity != "" {
			lvl, on, err := ParseLevel(s.Severity)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			if !on {
				continue // severity "off" disables the rule
			}
			r.Level = lvl
		}
		if s.Match != "" {
			if r.match, err = compile(env, s.Match, cel.BoolType); err != nil {
				return nil, fmt.Errorf("%s: match: %w", where, err)
			}
		}
		if r.expr, err = compile(env, s.Expr, cel.BoolType); err != nil {
			return nil, fmt.Errorf("%s: expr: %w", where, err)
		}
		if s.MessageExpression != "" {
			if r.msg, err = compile(env, s.MessageExpression, cel.StringType); err != nil {
				return nil, fmt.Errorf("%s: messageExpression: %w", where, err)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func evalBool(p cel.Program, vars map[string]any) (bool, error) {
	v, _, err := p.Eval(vars)
	if err != nil {
		return false, err
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("non-boolean result: %v", v.Value())
	}
	return b, nil
}

func (r *CustomRule) message(vars map[string]any) string {
	if r.msg != nil {
		if v, _, err := r.msg.Eval(vars); err == nil {
			if s, ok := v.Value().(string); ok {
				return s
			}
		}
	}
	return firstNonEmpty(r.Message, r.Description, "expression not satisfied: "+r.Expr)
}

func (r *CustomRule) list(ctx context.Context, env *Env) ([]unstructured.Unstructured, error) {
	opts := metav1.ListOptions{LabelSelector: r.LabelSelector, FieldSelector: r.FieldSelector}
	if len(r.Namespaces) == 0 {
		l, err := env.Dyn.Resource(r.GVR).Namespace("").List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return l.Items, nil
	}
	var all []unstructured.Unstructured
	for _, ns := range r.Namespaces {
		l, err := env.Dyn.Resource(r.GVR).Namespace(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, l.Items...)
	}
	return all, nil
}

// CheckCustomRules evaluates the CEL rules declared in the configuration.
func CheckCustomRules(ctx context.Context, env *Env, o *Options) *report.Section {
	s := report.NewSection("custom", "Custom rules")
	if len(o.CustomRules) == 0 {
		return s.Skipped("no custom rule (customRules section of --config)")
	}
	r := newRuler(s, o)
	t := s.NewTable("Violations", "Rule", "Namespace", "Object", "Message")
	base := map[string]any{
		"now":                time.Now(),
		"clusterVersion":     env.ClusterVersion,
		"hostedControlPlane": env.HostedControlPlane,
		"object":             map[string]any{},
		"objects":            []any{},
	}
	var totalViolations int
	for _, rule := range o.CustomRules {
		items, err := rule.list(ctx, env)
		if err != nil {
			if notInstalled(err) {
				s.Add(report.Skip, "%s: API %s/%s not served by this cluster", rule.ID, rule.APIVersion, rule.Resource)
			} else {
				s.AddRule(rule.ID, report.Error, fmt.Sprintf("%s: cannot list objects: %v", rule.ID, err))
			}
			continue
		}
		params := rule.Params
		if params == nil {
			params = map[string]any{}
		}
		vars := func(extra map[string]any) map[string]any {
			m := map[string]any{"params": params}
			for k, v := range base {
				m[k] = v
			}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}

		var selected []unstructured.Unstructured
		var evalErrs int
		var firstErr error
		for _, it := range items {
			if rule.match == nil {
				selected = append(selected, it)
				continue
			}
			ok, err := evalBool(rule.match, vars(map[string]any{"object": it.Object}))
			if err != nil {
				evalErrs++
				if firstErr == nil {
					firstErr = fmt.Errorf("match on %s/%s: %w", it.GetNamespace(), it.GetName(), err)
				}
				continue
			}
			if ok {
				selected = append(selected, it)
			}
		}

		violations := 0
		if rule.Aggregate {
			objs := make([]any, 0, len(selected))
			for _, it := range selected {
				objs = append(objs, it.Object)
			}
			v := vars(map[string]any{"objects": objs})
			ok, err := evalBool(rule.expr, v)
			switch {
			case err != nil:
				evalErrs++
				firstErr = err
			case !ok:
				if _, kept := r.row(t, rule.ID, "", "", rule.ID, "-", fmt.Sprintf("(%d objects)", len(selected)), oneLine(rule.message(v), 200)); kept {
					violations++
				}
			}
		} else {
			for _, it := range selected {
				v := vars(map[string]any{"object": it.Object})
				ok, err := evalBool(rule.expr, v)
				if err != nil {
					evalErrs++
					if firstErr == nil {
						firstErr = fmt.Errorf("%s/%s: %w", it.GetNamespace(), it.GetName(), err)
					}
					continue
				}
				if !ok {
					if _, kept := r.row(t, rule.ID, it.GetNamespace(), it.GetName(), rule.ID, orDash(it.GetNamespace()), it.GetName(), oneLine(rule.message(v), 200)); kept {
						violations++
					}
				}
			}
		}
		totalViolations += violations
		if evalErrs > 0 {
			s.AddRule(rule.ID, report.Error, fmt.Sprintf("%s: evaluation failed on %d object(s): %v (use has() for optional fields)", rule.ID, evalErrs, firstErr))
		}
		lvl := report.Pass
		if violations > 0 {
			lvl = report.Info // rows already carry the level
		}
		s.Add(lvl, "%s — %s: %d violation(s) out of %d object(s)", rule.ID, oneLine(firstNonEmpty(rule.Description, rule.Expr), 80), violations, len(selected))
	}
	t.Limit(o.MaxRows)
	s.Summary = fmt.Sprintf("%d rule(s), %d violation(s)", len(o.CustomRules), totalViolations)
	r.done()
	return s
}
