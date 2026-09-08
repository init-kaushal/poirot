package opencost

import (
	"context"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Target struct {
	Namespace string
	Name      string
	Port      string // a port NAME, or a number as a string
	Scheme    string
}

var candidateNames = map[string]bool{
	"opencost": true, "kubecost-cost-analyzer": true, "cost-analyzer": true,
}

var knownPorts = map[int32]bool{9003: true, 9090: true}

func pickPort(ports []corev1.ServicePort) (port, scheme string, ok bool) {
	for _, p := range ports {
		switch strings.ToLower(p.Name) {
		case "http", "web", "http-web":
			return portString(p), "http", true
		}
	}
	for _, p := range ports {
		if strings.ToLower(p.Name) == "https" {
			return portString(p), "https", true
		}
	}
	if len(ports) == 1 {
		return portString(ports[0]), "http", true
	}
	for _, p := range ports {
		if knownPorts[p.Port] {
			return portString(p), "http", true
		}
	}
	return "", "", false
}

func portString(p corev1.ServicePort) string {
	if p.Name != "" {
		return p.Name
	}
	return strconv.Itoa(int(p.Port))
}

// Discover returns the first well-known OpenCost/Kubecost Service, or nil.
// It lists Services once (restricted to scopeNamespaces when non-empty, cluster
// -wide otherwise) and sorts by (namespace, name) for a stable pick.
func Discover(ctx context.Context, cs kubernetes.Interface, scopeNamespaces []string) (*Target, error) {
	var scope map[string]bool
	if len(scopeNamespaces) > 0 {
		scope = make(map[string]bool, len(scopeNamespaces))
		for _, ns := range scopeNamespaces {
			scope[ns] = true
		}
	}

	svcs, err := cs.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	items := append([]corev1.Service(nil), svcs.Items...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Namespace != items[j].Namespace {
			return items[i].Namespace < items[j].Namespace
		}
		return items[i].Name < items[j].Name
	})

	for _, s := range items {
		if scope != nil && !scope[s.Namespace] {
			continue
		}
		if !candidateNames[strings.ToLower(s.Name)] {
			continue
		}
		if port, scheme, ok := pickPort(s.Spec.Ports); ok {
			return &Target{Namespace: s.Namespace, Name: s.Name, Port: port, Scheme: scheme}, nil
		}
	}
	return nil, nil
}
