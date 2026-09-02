package report

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
)

func mkF(rule string, sev analyzer.Severity, obj string) analyzer.Finding {
	return analyzer.Finding{RuleID: rule, Domain: "reliability", Severity: sev,
		Object: analyzer.ObjectRef{Kind: "Pod", Namespace: "n", Name: obj}}
}

func TestBuildSortsAndCounts(t *testing.T) {
	findings := []analyzer.Finding{
		mkF("reliability/no-limits", analyzer.SeverityInfo, "b"),
		mkF("reliability/crashloop", analyzer.SeverityCritical, "z"),
		mkF("reliability/oomkilled", analyzer.SeverityWarning, "a"),
	}
	r := Build(Meta{Version: "1.0.0"}, nil, findings)

	require.Equal(t, "reliability/crashloop", r.Findings[0].RuleID)
	require.Equal(t, "reliability/oomkilled", r.Findings[1].RuleID)
	require.Equal(t, "reliability/no-limits", r.Findings[2].RuleID)
	require.Equal(t, Counts{Critical: 1, Warning: 1, Info: 1}, r.Meta.Counts)
	require.Equal(t, "poirot", r.Meta.Tool)
}

func TestExitCode(t *testing.T) {
	crit := Build(Meta{}, nil, []analyzer.Finding{mkF("x", analyzer.SeverityCritical, "a")})
	warn := Build(Meta{}, nil, []analyzer.Finding{mkF("x", analyzer.SeverityWarning, "a")})
	clean := Build(Meta{}, nil, nil)

	require.Equal(t, 2, crit.ExitCode("critical"))
	require.Equal(t, 0, warn.ExitCode("critical"))
	require.Equal(t, 1, warn.ExitCode("warning"))
	require.Equal(t, 2, crit.ExitCode("warning"))
	require.Equal(t, 0, crit.ExitCode("none"))
	require.Equal(t, 0, clean.ExitCode("warning"))
}

func TestBuildNilSlicesMarshalAsEmptyArrays(t *testing.T) {
	b, err := Build(Meta{Version: "v"}, nil, nil).JSON()
	require.NoError(t, err)
	s := string(b)

	require.Contains(t, s, "\"findings\": []")
	require.NotContains(t, s, "\"findings\": null")
	require.Contains(t, s, "\"connectors\": []")
	require.NotContains(t, s, "\"connectors\": null")
}

func TestJSONRoundTrips(t *testing.T) {
	r := Build(Meta{Version: "1", GeneratedAt: time.Now()},
		[]connector.Status{{Name: "k8s", Availability: connector.Availability{State: connector.StateAvailable}}},
		[]analyzer.Finding{mkF("x", analyzer.SeverityWarning, "a")})
	b, err := r.JSON()
	require.NoError(t, err)

	var back Report
	require.NoError(t, json.Unmarshal(b, &back))
	require.Equal(t, "k8s", back.Connectors[0].Name)
	require.Len(t, back.Findings, 1)
}
