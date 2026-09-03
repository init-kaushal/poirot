package promql

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
	Port      string
	Scheme    string
}

var candidateNames = map[string]bool{
	"prometheus": true, "prometheus-server": true, "prometheus-k8s": true,
	"prometheus-operated": true, "kube-prometheus-stack-prometheus": true,
	"thanos-query": true, "thanos-query-frontend": true,
	"victoria-metrics": true, "victoria-metrics-single-server": true,
	"vmsingle": true, "vmselect": true,
	"mimir": true, "mimir-query-frontend": true, "mimir-nginx": true,
}

// candidatePrefixes only holds prefixes with no redundant candidateNames entry.
// thanos-query / mimir-query were dropped: they duplicate exact names and only
// add prefix-shadow risk.
var candidatePrefixes = []string{
	"prometheus-", "vmselect-", "vmsingle-",
}

// denyComponents are non-query components of Prometheus-family charts that share
// a candidate prefix (notably "prometheus-alertmanager"). Checked in the prefix
// pass only.
var denyComponents = []string{
	"alertmanager", "node-exporter", "kube-state-metrics",
	"pushgateway", "operator", "blackbox", "grafana",
}

var knownPorts = map[int32]bool{9090: true, 8080: true, 8429: true, 8481: true, 10902: true, 9091: true}

func exactMatch(n string) bool {
	return candidateNames[strings.ToLower(n)]
}

func prefixMatch(n string) bool {
	n = strings.ToLower(n)
	for _, d := range denyComponents {
		if strings.Contains(n, d) {
			return false
		}
	}
	for _, p := range candidatePrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

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

func Discover(ctx context.Context, cs kubernetes.Interface, namespaces []string) (*Target, error) {
	nss := namespaces
	if len(nss) == 0 {
		list, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, n := range list.Items {
			nss = append(nss, n.Name)
		}
		sort.Strings(nss)
	}
	// Pass 1 takes any exact candidateNames hit anywhere before pass 2 falls back
	// to prefix matching, so "prometheus-server" always wins over a same-namespace
	// "prometheus-alertmanager" that only matches the "prometheus-" prefix.
	for _, match := range []func(string) bool{exactMatch, prefixMatch} {
		for _, ns := range nss {
			svcs, err := cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			items := append([]corev1.Service(nil), svcs.Items...)
			sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
			for _, s := range items {
				if !match(s.Name) {
					continue
				}
				if port, scheme, ok := pickPort(s.Spec.Ports); ok {
					return &Target{Namespace: s.Namespace, Name: s.Name, Port: port, Scheme: scheme}, nil
				}
			}
		}
	}
	return nil, nil
}
