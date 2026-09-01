package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/init-kaushal/poirot/internal/connector"
)

// Scope narrows what the connector inspects during collection.
type Scope struct {
	Namespaces []string
	Exclude    []string
	Lookback   time.Duration
}

// Connector is a read-only Kubernetes data provider.
type Connector struct {
	cs      kubernetes.Interface
	ctxName string
	scope   Scope
}

// New builds a Connector from a kubeconfig path (with a leading "~/" expanded)
// or from in-cluster config when kubeconfigPath is empty and running in a pod.
// contextName, when set, overrides the current context; the resolved context
// name is recorded on the Connector.
func New(kubeconfigPath, contextName string, scope Scope) (*Connector, error) {
	cfg, resolvedCtx, err := buildRESTConfig(kubeconfigPath, contextName)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build kubernetes client: %w", err)
	}
	return &Connector{cs: cs, ctxName: resolvedCtx, scope: scope}, nil
}

// NewWithClient injects a client (tests).
func NewWithClient(cs kubernetes.Interface, contextName string, scope Scope) *Connector {
	return &Connector{cs: cs, ctxName: contextName, scope: scope}
}

func buildRESTConfig(path, ctxName string) (*rest.Config, string, error) {
	if path == "" {
		if c, err := rest.InClusterConfig(); err == nil {
			return c, "in-cluster", nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = expandHome(path)
	}
	overrides := &clientcmd.ConfigOverrides{}
	if ctxName != "" {
		overrides.CurrentContext = ctxName
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restCfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	resolved := ctxName
	if resolved == "" {
		if raw, err := cc.RawConfig(); err == nil {
			resolved = raw.CurrentContext
		}
	}
	return restCfg, resolved, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// Name returns the connector's stable identifier.
func (c *Connector) Name() string { return "k8s" }

// ContextName returns the resolved kubeconfig context name.
func (c *Connector) ContextName() string { return c.ctxName }

// Probe reports whether the Kubernetes API server is reachable.
func (c *Connector) Probe(ctx context.Context) connector.Availability {
	if _, err := c.cs.Discovery().ServerVersion(); err != nil {
		return connector.Availability{
			State:  connector.StateAbsent,
			Reason: "cannot reach Kubernetes API server",
			Detail: err.Error(),
		}
	}
	return connector.Availability{State: connector.StateAvailable}
}

// Capabilities returns the queryable capabilities (wired in M3).
func (c *Connector) Capabilities() []connector.Capability { return nil }

// Query is unsupported in M1.
func (c *Connector) Query(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("k8s connector exposes no queryable capabilities in M1")
}
