// Package reports generates benchmark result reports in various formats.
package reports

import (
	"html/template"
	"io"
	"sort"
)

// CellData holds the metrics for a single (harness, category) cell in the report.
type CellData struct {
	Harness       string  `json:"harness"`
	Category      string  `json:"category"`
	TaskCount     int     `json:"task_count"`
	SuccessRate   float64 `json:"success_rate"`
	HonestyScore  float64 `json:"honesty_score"`
	CostUSD       float64 `json:"cost_usd"`
	CheatingRate  float64 `json:"cheating_rate"`
}

// ReportData holds the full dataset for report generation.
type ReportData struct {
	Title      string     `json:"title"`
	RunID      string     `json:"run_id"`
	Timestamp  string     `json:"timestamp"`
	Cells      []CellData `json:"cells"`
	Harnesses  []string   `json:"harnesses"`
	Categories []string   `json:"categories"`
}

// BuildReportData organizes raw cell data into a ReportData with sorted
// harnesses and categories extracted from the cells.
func BuildReportData(title, runID, timestamp string, cells []CellData) ReportData {
	harnessSet := make(map[string]bool)
	categorySet := make(map[string]bool)
	for _, c := range cells {
		harnessSet[c.Harness] = true
		categorySet[c.Category] = true
	}

	harnesses := make([]string, 0, len(harnessSet))
	for h := range harnessSet {
		harnesses = append(harnesses, h)
	}
	sort.Strings(harnesses)

	categories := make([]string, 0, len(categorySet))
	for c := range categorySet {
		categories = append(categories, c)
	}
	sort.Strings(categories)

	return ReportData{
		Title:      title,
		RunID:      runID,
		Timestamp:  timestamp,
		Cells:      cells,
		Harnesses:  harnesses,
		Categories: categories,
	}
}

const htmlTpl = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<style>
body { font-family: sans-serif; margin: 2em; }
h1 { color: #333; }
table { border-collapse: collapse; margin-top: 1em; }
th, td { border: 1px solid #ccc; padding: 8px 12px; text-align: right; }
th { background: #f5f5f5; text-align: center; }
td.label { text-align: left; font-weight: bold; }
.good { background: #e6ffe6; }
.warn { background: #fff9e6; }
.bad { background: #ffe6e6; }
.meta { color: #666; font-size: 0.9em; margin-bottom: 1em; }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<div class="meta">Run: {{.RunID}} | Generated: {{.Timestamp}}</div>
{{range $cat := .Categories}}
<h2>{{$cat}}</h2>
<table>
<tr>
  <th>Harness</th>
  <th>Tasks</th>
  <th>Success Rate</th>
  <th>Honesty</th>
  <th>Cost (USD)</th>
  <th>Cheating Rate</th>
</tr>
{{range $cell := $.Cells}}{{if eq $cell.Category $cat}}
<tr>
  <td class="label">{{$cell.Harness}}</td>
  <td>{{$cell.TaskCount}}</td>
  <td class="{{successClass $cell.SuccessRate}}">{{printf "%.1f%%" (pct $cell.SuccessRate)}}</td>
  <td class="{{honestyClass $cell.HonestyScore}}">{{printf "%.2f" $cell.HonestyScore}}</td>
  <td>{{printf "$%.4f" $cell.CostUSD}}</td>
  <td class="{{cheatingClass $cell.CheatingRate}}">{{printf "%.1f%%" (pct $cell.CheatingRate)}}</td>
</tr>
{{end}}{{end}}
</table>
{{end}}
</body>
</html>`

// WriteHTML writes an HTML report to w.
func WriteHTML(w io.Writer, data ReportData) error {
	funcMap := template.FuncMap{
		"pct": func(f float64) float64 { return f * 100 },
		"successClass": func(f float64) string {
			switch {
			case f >= 0.8:
				return "good"
			case f >= 0.5:
				return "warn"
			default:
				return "bad"
			}
		},
		"honestyClass": func(f float64) string {
			switch {
			case f >= 0.9:
				return "good"
			case f >= 0.7:
				return "warn"
			default:
				return "bad"
			}
		},
		"cheatingClass": func(f float64) string {
			switch {
			case f <= 0.01:
				return "good"
			case f <= 0.05:
				return "warn"
			default:
				return "bad"
			}
		},
	}

	t, err := template.New("report").Funcs(funcMap).Parse(htmlTpl)
	if err != nil {
		return err
	}
	return t.Execute(w, data)
}
