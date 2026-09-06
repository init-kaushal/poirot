package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/init-kaushal/poirot/internal/analyzer"
)

type mdDomain struct {
	Name     string
	Findings []analyzer.Finding
}

type mdData struct {
	Meta       Meta
	Connectors []connectorRow
	Domains    []mdDomain
	HasContent bool
	Skipped    []string
	Summary    *Summary
	LLMBanner  string
}

type connectorRow struct {
	Name   string
	State  string
	Detail string
}

var mdFuncs = template.FuncMap{
	"sevTag": func(s analyzer.Severity) string { return strings.ToUpper(string(s)) },
	"evline": func(e analyzer.Evidence) string {
		v, _ := json.Marshal(e.Value)
		return fmt.Sprintf("`%s` → %s (%s)", e.Query, string(v), e.Source)
	},
	"add": func(a, b int) int { return a + b },
}

var mdTmpl = template.Must(template.New("report").Funcs(mdFuncs).Parse(`# poirot report

**{{.Meta.Context}}** · lookback {{.Meta.Lookback}} · generated {{.Meta.GeneratedAt.Format "2006-01-02T15:04:05Z07:00"}} · poirot {{.Meta.Version}}

**{{.Meta.Counts.Critical}} critical · {{.Meta.Counts.Warning}} warning · {{.Meta.Counts.Info}} info**
{{- if .LLMBanner}}
{{.LLMBanner}}
{{- end}}

## Connector coverage

| Connector | State | Detail |
| --- | --- | --- |
{{- range .Connectors}}
| {{.Name}} | {{.State}} | {{.Detail}} |
{{- end}}

{{if .Summary}}## Summary

{{.Summary.Headline}}
{{range $i, $a := .Summary.Actions}}
{{add $i 1}}. {{$a}}
{{- end}}

{{end}}## Findings
{{if not .HasContent}}{{if .Skipped}}
No findings from the checks that ran.
{{else}}
No findings. ✅
{{end}}{{else}}{{range .Domains}}
### {{.Name}}
{{range .Findings}}
#### [{{sevTag .Severity}}] {{.Title}} — {{.Object.String}}

{{.Summary}}
{{if .Evidence}}
Evidence:
{{- range .Evidence}}
- {{evline .}}
{{- end}}
{{- end}}
{{if .Analysis}}
Probable cause: {{.Analysis.ProbableCause}}
Remediation: {{.Analysis.Remediation}}
Confidence: {{.Analysis.Confidence}}
{{if .Analysis.CorrelatedFindings}}Correlated: {{range $i, $c := .Analysis.CorrelatedFindings}}{{if $i}}, {{end}}{{$c}}{{end}}
{{end}}{{end}}
{{- end}}{{end}}{{end}}
## Checks skipped
{{if .Skipped}}{{range .Skipped}}
- {{.}}
{{- end}}
{{else}}
All configured checks ran.
{{end}}`))

func (r Report) Markdown() ([]byte, error) {
	rows := make([]connectorRow, 0, len(r.Connectors))
	for _, s := range r.Connectors {
		parts := make([]string, 0, 2)
		if s.Availability.Reason != "" {
			parts = append(parts, s.Availability.Reason)
		}
		if s.Availability.Detail != "" {
			parts = append(parts, s.Availability.Detail)
		}
		detail := strings.ReplaceAll(strings.Join(parts, " — "), "|", `\|`)
		if detail == "" {
			detail = "-"
		}
		rows = append(rows, connectorRow{Name: s.Name, State: string(s.Availability.State), Detail: detail})
	}

	byDomain := map[string][]analyzer.Finding{}
	var skipped []string
	for _, f := range r.Findings {
		if strings.HasSuffix(f.RuleID, "/skipped") {
			skipped = append(skipped, f.Summary)
			continue
		}
		byDomain[f.Domain] = append(byDomain[f.Domain], f)
	}
	names := make([]string, 0, len(byDomain))
	for n := range byDomain {
		names = append(names, n)
	}
	sort.Strings(names)
	domains := make([]mdDomain, 0, len(names))
	for _, n := range names {
		domains = append(domains, mdDomain{Name: n, Findings: byDomain[n]})
	}

	var banner string
	if r.Meta.LLM != nil && (strings.HasPrefix(r.Meta.LLM.Status, "skipped") || strings.HasPrefix(r.Meta.LLM.Status, "partial")) {
		banner = "> ⚠️ AI analysis " + r.Meta.LLM.Status
	}

	data := mdData{
		Meta:       r.Meta,
		Connectors: rows,
		Domains:    domains,
		HasContent: len(domains) > 0,
		Skipped:    skipped,
		Summary:    r.Summary,
		LLMBanner:  banner,
	}
	var buf bytes.Buffer
	if err := mdTmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
