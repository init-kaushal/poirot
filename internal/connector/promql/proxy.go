package promql

import (
	"context"
	"net/url"

	"k8s.io/client-go/kubernetes"
)

func flattenParams(p url.Values) map[string]string {
	m := make(map[string]string, len(p))
	for k, v := range p {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m
}

// proxyDoer reaches the metrics backend Service through the Kubernetes
// API-server proxy subresource. Read-only: ProxyGet issues an HTTP GET.
func proxyDoer(cs kubernetes.Interface, t Target) doer {
	return func(ctx context.Context, path string, params url.Values) ([]byte, error) {
		return cs.CoreV1().
			Services(t.Namespace).
			ProxyGet(t.Scheme, t.Name, t.Port, path, flattenParams(params)).
			DoRaw(ctx)
	}
}
