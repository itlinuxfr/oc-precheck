// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

package checks

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/itlinuxfr/oc-precheck/internal/report"
)

// Banner defaults.
const (
	DefaultBannerName = "oc-precheck-maintenance"
	managedByLabel    = "app.kubernetes.io/managed-by"
	managedByValue    = "oc-precheck"
)

// BannerOptions configures a ConsoleNotification (web console banner).
type BannerOptions struct {
	Name       string
	Text       string
	Location   string // BannerTop | BannerBottom | BannerTopBottom
	Color      string
	Background string
	LinkText   string
	LinkURL    string
	DryRun     bool
}

// ActionBanner creates or updates a console banner.
func ActionBanner(ctx context.Context, env *Env, bo BannerOptions) *report.Section {
	s := report.NewSection("banner", "Action: web console banner")
	switch bo.Location {
	case "", "BannerTop", "BannerBottom", "BannerTopBottom":
	default:
		return s.Fail(fmt.Errorf("invalid location %q", bo.Location), "use BannerTop, BannerBottom or BannerTopBottom")
	}
	if bo.Location == "" {
		bo.Location = "BannerTop"
	}
	if bo.Name == "" {
		bo.Name = DefaultBannerName
	}
	if bo.Text == "" {
		return s.Fail(fmt.Errorf("empty banner text"), "")
	}
	spec := map[string]any{
		"text":            bo.Text,
		"location":        bo.Location,
		"color":           bo.Color,
		"backgroundColor": bo.Background,
	}
	if bo.LinkURL != "" {
		spec["link"] = map[string]any{"href": bo.LinkURL, "text": firstNonEmpty(bo.LinkText, bo.LinkURL)}
	}
	s.Summary = fmt.Sprintf("ConsoleNotification %q (%s): %q", bo.Name, bo.Location, bo.Text)
	if bo.DryRun {
		s.Add(report.Info, "dry run: banner not created")
		return s
	}
	res := env.Dyn.Resource(gvrConsoleNotif)
	existing, err := res.Get(ctx, bo.Name, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Object["spec"] = spec
		if _, err := res.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return s.Fail(err, "")
		}
		s.Add(report.Pass, "Banner %q updated", bo.Name)
	case apierrors.IsNotFound(err):
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "console.openshift.io/v1",
			"kind":       "ConsoleNotification",
			"metadata": map[string]any{
				"name":   bo.Name,
				"labels": map[string]any{managedByLabel: managedByValue},
			},
			"spec": spec,
		}}
		if _, err := res.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			if notInstalled(err) {
				return s.Fail(err, "console.openshift.io API not served (console disabled?)")
			}
			return s.Fail(err, "")
		}
		s.Add(report.Pass, "Banner %q created", bo.Name)
	case notInstalled(err):
		return s.Fail(err, "console.openshift.io API not served (console disabled?)")
	default:
		return s.Fail(err, "")
	}
	s.Add(report.Info, "To remove it: oc precheck banner remove --name %s", bo.Name)
	return s
}

// ActionRemoveBanner deletes console banners: the named ones, or every
// banner created by this tool when all is set.
func ActionRemoveBanner(ctx context.Context, env *Env, names []string, all, dryRun bool) *report.Section {
	s := report.NewSection("banner-remove", "Action: remove web console banner(s)")
	res := env.Dyn.Resource(gvrConsoleNotif)
	if all {
		list, err := res.List(ctx, metav1.ListOptions{LabelSelector: managedByLabel + "=" + managedByValue})
		if err != nil {
			return s.Fail(err, "")
		}
		names = nil
		for _, n := range list.Items {
			names = append(names, n.GetName())
		}
		if len(names) == 0 {
			s.Summary = "No banner created by oc-precheck"
			return s
		}
	}
	if len(names) == 0 {
		names = []string{DefaultBannerName}
	}
	s.Summary = fmt.Sprintf("%d banner(s)", len(names))
	for _, name := range names {
		if dryRun {
			s.Add(report.Info, "dry run: banner %q would be removed", name)
			continue
		}
		err := res.Delete(ctx, name, metav1.DeleteOptions{})
		switch {
		case err == nil:
			s.Add(report.Pass, "Banner %q removed", name)
		case apierrors.IsNotFound(err):
			s.Add(report.Info, "Banner %q not found", name)
		default:
			s.Add(report.Error, "Banner %q: %v", name, err)
		}
	}
	return s
}

// ListBanners shows the ConsoleNotifications of the cluster.
func ListBanners(ctx context.Context, env *Env) *report.Section {
	s := report.NewSection("banners", "Web console banners (ConsoleNotification)")
	list, err := env.Dyn.Resource(gvrConsoleNotif).List(ctx, metaList)
	if err != nil {
		if notInstalled(err) {
			return s.Skipped("console.openshift.io API not served")
		}
		return s.Fail(err, "")
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
	t := s.NewTable("", "Name", "Location", "Text", "Colors", "Link", "Managed by")
	for _, n := range items {
		text, _, _ := unstructured.NestedString(n.Object, "spec", "text")
		loc, _, _ := unstructured.NestedString(n.Object, "spec", "location")
		fg, _, _ := unstructured.NestedString(n.Object, "spec", "color")
		bg, _, _ := unstructured.NestedString(n.Object, "spec", "backgroundColor")
		href, _, _ := unstructured.NestedString(n.Object, "spec", "link", "href")
		t.AddRow(report.Info, n.GetName(), orDash(loc), oneLine(text, 70), orDash(fg)+" / "+orDash(bg), orDash(href),
			orDash(n.GetLabels()[managedByLabel]))
	}
	s.Summary = fmt.Sprintf("%d banner(s)", len(items))
	return s
}
