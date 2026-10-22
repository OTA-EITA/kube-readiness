package checks

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/OTA-EITA/kube-readiness/internal/workload"
)

// IgnoreAnnotation lists check IDs (comma separated) to skip for a workload.
const IgnoreAnnotation = "kube-readiness/ignore"

// Config tunes severities and exempts workloads from checks.
type Config struct {
	// Checks overrides a check's severity: danger, warning or ignore.
	Checks     map[string]Severity `yaml:"checks"`
	Exemptions []Exemption         `yaml:"exemptions"`
}

// Exemption skips checks for matching workloads. Empty lists match everything;
// names and namespaces accept "*" wildcards.
type Exemption struct {
	Checks     []string `yaml:"checks"`
	Namespaces []string `yaml:"namespaces"`
	Names      []string `yaml:"names"`
	Kinds      []string `yaml:"kinds"`
}

// ParseConfig reads a YAML config and validates check IDs and severities.
func ParseConfig(data []byte) (Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	known := map[string]bool{}
	for _, ch := range All() {
		known[ch.ID] = true
	}
	for id, sev := range c.Checks {
		if !known[id] {
			return c, fmt.Errorf("config: unknown check %q", id)
		}
		if sev != Danger && sev != Warning && sev != Ignore {
			return c, fmt.Errorf("config: check %s: severity must be danger, warning or ignore, got %q", id, sev)
		}
	}
	for i, e := range c.Exemptions {
		for _, id := range e.Checks {
			if !known[id] {
				return c, fmt.Errorf("config: exemptions[%d]: unknown check %q", i, id)
			}
		}
	}
	return c, nil
}

func (e Exemption) matches(checkID string, w workload.Workload) bool {
	return matchList(e.Checks, checkID) && matchList(e.Namespaces, w.Namespace) &&
		matchList(e.Names, w.Name) && matchList(e.Kinds, w.Kind)
}

func matchList(patterns []string, v string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if wildcard(p, v) {
			return true
		}
	}
	return false
}

// wildcard matches with "*" as the only special character.
func wildcard(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i, p := range parts[1:] {
		if i == len(parts)-2 {
			return strings.HasSuffix(s, p)
		}
		idx := strings.Index(s, p)
		if idx < 0 {
			return false
		}
		s = s[idx+len(p):]
	}
	return true
}

// Result is the outcome of one check on one workload.
type Result struct {
	ID       string   `json:"id"`
	Category Category `json:"category"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Passed   bool     `json:"passed"`
	Message  string   `json:"message,omitempty"`
}

// WorkloadReport scores one workload.
type WorkloadReport struct {
	Workload workload.Workload `json:"workload"`
	ID       string            `json:"id"`
	Results  []Result          `json:"results"`
	Score    int               `json:"score"`
	Grade    string            `json:"grade"`
}

// Failed returns the failed results, dangers first.
func (r WorkloadReport) Failed() []Result {
	var out []Result
	for _, res := range r.Results {
		if !res.Passed {
			out = append(out, res)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity.Weight() > out[j].Severity.Weight() })
	return out
}

// Report is the result for every workload.
type Report struct {
	Workloads []WorkloadReport `json:"workloads"`
	Score     int              `json:"score"`
	Grade     string           `json:"grade"`
	Category  map[Category]int `json:"categoryScores"`
	Counts    map[Severity]int `json:"failedCounts"`
}

// Evaluate runs every applicable check against every workload.
func Evaluate(set workload.Set, cfg Config) Report {
	all := All()
	rep := Report{Category: map[Category]int{}, Counts: map[Severity]int{Danger: 0, Warning: 0}}
	totalPassed, totalWeight := 0, 0
	catPassed, catWeight := map[Category]int{}, map[Category]int{}

	for _, w := range set.Workloads {
		ignored := map[string]bool{}
		for _, id := range strings.Split(w.Annotations[IgnoreAnnotation], ",") {
			ignored[strings.TrimSpace(id)] = true
		}
		wr := WorkloadReport{Workload: w, ID: w.ID()}
		passed, weight := 0, 0
		for _, ch := range all {
			sev := ch.Severity
			if s, ok := cfg.Checks[ch.ID]; ok {
				sev = s
			}
			if sev == Ignore || ignored[ch.ID] || exempt(cfg, ch.ID, w) || (ch.Applies != nil && !ch.Applies(w)) {
				continue
			}
			msg := ch.Eval(w, set)
			res := Result{ID: ch.ID, Category: ch.Category, Severity: sev, Title: ch.Title, Passed: msg == "", Message: msg}
			wr.Results = append(wr.Results, res)
			weight += sev.Weight()
			catWeight[ch.Category] += sev.Weight()
			if res.Passed {
				passed += sev.Weight()
				catPassed[ch.Category] += sev.Weight()
			} else {
				rep.Counts[sev]++
			}
		}
		wr.Score = percent(passed, weight)
		wr.Grade = grade(wr.Score)
		totalPassed += passed
		totalWeight += weight
		rep.Workloads = append(rep.Workloads, wr)
	}
	rep.Score = percent(totalPassed, totalWeight)
	rep.Grade = grade(rep.Score)
	for c, wgt := range catWeight {
		rep.Category[c] = percent(catPassed[c], wgt)
	}
	sort.SliceStable(rep.Workloads, func(i, j int) bool {
		if rep.Workloads[i].Score != rep.Workloads[j].Score {
			return rep.Workloads[i].Score < rep.Workloads[j].Score
		}
		return rep.Workloads[i].ID < rep.Workloads[j].ID
	})
	return rep
}

func exempt(cfg Config, id string, w workload.Workload) bool {
	for _, e := range cfg.Exemptions {
		if e.matches(id, w) {
			return true
		}
	}
	return false
}

func percent(passed, total int) int {
	if total == 0 {
		return 100
	}
	return int(math.Floor(100 * float64(passed) / float64(total)))
}

func grade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 60:
		return "D"
	default:
		return "F"
	}
}
