// Copyright 2026 The oc-precheck Authors.
// SPDX-License-Identifier: Apache-2.0

// Package kube builds Kubernetes clients from the standard kubeconfig
// loading rules and override flags, and provides a pod exec helper.
package kube

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/itlinuxfr/oc-precheck/internal/version"
)

// Executor runs a command inside a container and returns stdout/stderr.
type Executor interface {
	Exec(ctx context.Context, namespace, pod, container string, cmd []string) (stdout, stderr string, err error)
}

// Flags holds the standard kubectl connection flags (--kubeconfig,
// --context, --server, --token, --as, --insecure-skip-tls-verify,
// --request-timeout, ...).
type Flags struct {
	Rules     *clientcmd.ClientConfigLoadingRules
	Overrides *clientcmd.ConfigOverrides
}

// NewFlags creates the flags with the default loading rules
// (--kubeconfig, then $KUBECONFIG, then ~/.kube/config).
func NewFlags() *Flags {
	return &Flags{Rules: clientcmd.NewDefaultClientConfigLoadingRules(), Overrides: &clientcmd.ConfigOverrides{}}
}

// AddFlags binds the flags to a flag set. --namespace is deliberately not
// exposed: checks are cluster-wide.
func (f *Flags) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&f.Rules.ExplicitPath, clientcmd.RecommendedConfigPathFlag, "", "Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	names := clientcmd.RecommendedConfigOverrideFlags("")
	names.ContextOverrideFlags.Namespace.LongName = ""
	names.ContextOverrideFlags.Namespace.ShortName = ""
	clientcmd.BindOverrideFlags(f.Overrides, fs, names)
}

// Client bundles the clients used by the checks.
type Client struct {
	Config  *rest.Config
	CS      kubernetes.Interface
	Dyn     dynamic.Interface
	Context string
	Server  string
}

// New builds the clients the same way kubectl / oc do.
func (f *Flags) New() (*Client, error) {
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(f.Rules, f.Overrides)
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	ctxName := f.Overrides.CurrentContext
	if ctxName == "" {
		if raw, err := cc.RawConfig(); err == nil {
			ctxName = raw.CurrentContext
		}
	}
	cfg.QPS = 50
	cfg.Burst = 100
	cfg.UserAgent = "oc-precheck/" + version.Get().Version

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{Config: cfg, CS: cs, Dyn: dyn, Context: ctxName, Server: cfg.Host}, nil
}

// Exec runs cmd in the given container. It tries WebSocket first (default
// since Kubernetes 1.30 / OpenShift 4.17) and falls back to SPDY, like
// recent kubectl/oc, which helps behind corporate proxies.
func (c *Client) Exec(ctx context.Context, namespace, pod, container string, cmd []string) (string, string, error) {
	// Exec streams must not be cut by the per-request timeout.
	cfg := rest.CopyConfig(c.Config)
	cfg.Timeout = 0

	req := c.CS.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	spdy, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return "", "", err
	}
	ws, err := remotecommand.NewWebSocketExecutor(cfg, "GET", req.URL().String())
	if err != nil {
		return "", "", err
	}
	ex, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return "", "", err
	}

	var out, errb bytes.Buffer
	if err := ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &out, Stderr: &errb}); err != nil {
		return out.String(), errb.String(), fmt.Errorf("exec %s/%s[%s] %q: %w (stderr: %s)",
			namespace, pod, container, strings.Join(cmd, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), errb.String(), nil
}
