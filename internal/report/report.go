// Package report renders readiness results as a table, JSON, Markdown or SARIF.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/OTA-EITA/kube-readiness/internal/checks"
)

// Format is an output format.
type Format string

// Supported formats.
const (
	Table    Format = "table"
	JSON     Format = "json"
	Markdown Format = "markdown"
	SARIF    Format = "sarif"
)

// ParseFormat validates a format name.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case Table, JSON, Markdown, SARIF:
		return Format(s), nil
	}
	return "", fmt.Errorf("unknown output format %q (want table, json, markdown or sarif)", s)
}

// Write renders the report.
func Write(w io.Writer, r checks.Report, f Format, version string) error {
	switch f {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case Markdown:
		return writeMarkdown(w, r)
	case SARIF:
		return writeSARIF(w, r, version)
	default:
		return writeTable(w, r)
	}
}

func summary(r checks.Report) string {
	return fmt.Sprintf("Score %d (%s)  reliability %d  efficiency %d  security %d  |  %d workloads, %d danger, %d warning",
		r.Score, r.Grade, r.Category[checks.Reliability], r.Category[checks.Efficiency], r.Category[checks.Security],
		len(r.Workloads), r.Counts[checks.Danger], r.Counts[checks.Warning])
}

func writeTable(w io.Writer, r checks.Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tGRADE\tWORKLOAD\tFAILED CHECKS")
	for _, wr := range r.Workloads {
		var ids []string
		for _, res := range wr.Failed() {
			mark := ""
			if res.Severity == checks.Danger {
				mark = "!"
			}
			ids = append(ids, res.ID+mark)
		}
		failed := strings.Join(ids, " ")
		if failed == "" {
			failed = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", wr.Score, wr.Grade, wr.ID, failed)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\n%s\n(! = danger. Run with --output markdown for details.)\n", summary(r))
	return err
}

func writeMarkdown(w io.Writer, r checks.Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "## kube-readiness: %d (%s)\n\n%s\n\n", r.Score, r.Grade, summary(r))
	b.WriteString("| Score | Workload | Severity | Check | Problem |\n|---|---|---|---|---|\n")
	for _, wr := range r.Workloads {
		failed := wr.Failed()
		if len(failed) == 0 {
			fmt.Fprintf(&b, "| %d (%s) | `%s` | | | ✅ all checks passed |\n", wr.Score, wr.Grade, wr.ID)
			continue
		}
		for i, res := range failed {
			head := ""
			if i == 0 {
				head = fmt.Sprintf("%d (%s) | `%s`", wr.Score, wr.Grade, wr.ID)
			} else {
				head = " | "
			}
			fmt.Fprintf(&b, "| %s | %s | %s %s | %s |\n", head, res.Severity, res.ID, res.Title, strings.ReplaceAll(res.Message, "|", `\|`))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    map[string]any `json:"tool"`
	Results []sarifResult  `json:"results"`
}

type sarifResult struct {
	RuleID    string            `json:"ruleId"`
	Level     string            `json:"level"`
	Message   map[string]string `json:"message"`
	Locations []map[string]any  `json:"locations"`
}

func writeSARIF(w io.Writer, r checks.Report, version string) error {
	var rules []map[string]any
	for _, c := range checks.All() {
		level, score := "note", "3.0"
		if c.Severity == checks.Danger {
			level, score = "error", "7.0"
		} else if c.Category == checks.Security {
			level, score = "warning", "4.0"
		}
		rules = append(rules, map[string]any{
			"id":                   c.ID,
			"name":                 c.ID,
			"shortDescription":     map[string]string{"text": c.Title},
			"defaultConfiguration": map[string]string{"level": level},
			"properties":           map[string]any{"tags": []string{"kubernetes", string(c.Category)}, "security-severity": score},
		})
	}
	results := []sarifResult{}
	for _, wr := range r.Workloads {
		src := wr.Workload.Source
		if src.File == "" {
			continue // objects read from a cluster have no file to annotate
		}
		for _, res := range wr.Failed() {
			level := "warning"
			if res.Severity == checks.Danger {
				level = "error"
			}
			loc := map[string]any{"physicalLocation": map[string]any{
				"artifactLocation": map[string]string{"uri": src.File},
				"region":           map[string]int{"startLine": max(src.Line, 1)},
			}}
			results = append(results, sarifResult{
				RuleID: res.ID, Level: level,
				Message:   map[string]string{"text": wr.ID + ": " + res.Message},
				Locations: []map[string]any{loc},
			})
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(sarifLog{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool: map[string]any{"driver": map[string]any{
				"name": "kube-readiness", "version": version,
				"informationUri": "https://github.com/OTA-EITA/kube-readiness", "rules": rules,
			}},
			Results: results,
		}},
	})
}
