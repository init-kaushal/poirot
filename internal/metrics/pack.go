package metrics

type Kind int

const (
	KindInstant Kind = iota
	KindRange
)

type Entry struct {
	Name string
	Expr string
	Kind Kind
}

func DefaultPack() []Entry {
	return []Entry{
		{
			Name: "cpu_saturation", Kind: KindInstant,
			Expr: `max by (namespace, pod, container) (rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m]) / (container_spec_cpu_quota{container!="",container!="POD"} / container_spec_cpu_period{container!="",container!="POD"}))`,
		},
		{
			Name: "mem_saturation", Kind: KindInstant,
			Expr: `max by (namespace, pod, container) (container_memory_working_set_bytes{container!="",container!="POD"} / container_spec_memory_limit_bytes{container!="",container!="POD"} > 0)`,
		},
		{
			Name: "restart_rate", Kind: KindInstant,
			Expr: `max by (namespace, pod) (rate(kube_pod_container_status_restarts_total[1h]) * 3600)`,
		},
		{
			Name: "pod_not_ready", Kind: KindInstant,
			Expr: `max by (namespace, pod) (kube_pod_status_ready{condition="true"} == 0)`,
		},
		{
			Name: "targets_down", Kind: KindInstant,
			Expr: `up == 0`,
		},
	}
}
