package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/OTA-EITA/kube-readiness/internal/workload"
)

func load(t *testing.T, file string) workload.Set {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../examples", filepath.Base(file)))
	if err != nil {
		t.Fatal(err)
	}
	set, err := workload.LoadManifests(data, file)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func failedIDs(r WorkloadReport) string {
	var ids []string
	for _, res := range r.Results {
		if !res.Passed {
			ids = append(ids, res.ID)
		}
	}
	return strings.Join(ids, ",")
}

func byID(rep Report) map[string]WorkloadReport {
	m := map[string]WorkloadReport{}
	for _, w := range rep.Workloads {
		m[w.ID] = w
	}
	return m
}

func TestGoodExampleScoresFull(t *testing.T) {
	rep := Evaluate(load(t, "good.yaml"), Config{})
	if rep.Score != 100 || rep.Grade != "A" {
		for _, w := range rep.Workloads {
			t.Logf("%s failed: %s", w.ID, failedIDs(w))
		}
		t.Fatalf("score %d grade %s", rep.Score, rep.Grade)
	}
}

func TestBadExample(t *testing.T) {
	rep := Evaluate(load(t, "bad.yaml"), Config{})
	got := byID(rep)
	want := map[string]string{
		"Deployment/readiness-bad/api":       "REL-001,REL-002,REL-005,REL-006,REL-007,EFF-001,EFF-002,EFF-003,SEC-003,SEC-004,SEC-005,SEC-006,SEC-009,SEC-010",
		"DaemonSet/readiness-bad/node-agent": "REL-001,REL-002,EFF-001,EFF-002,EFF-003,SEC-001,SEC-002,SEC-003,SEC-004,SEC-005,SEC-006,SEC-007,SEC-008,SEC-009,SEC-010",
		"StatefulSet/readiness-bad/cache":    "REL-001,REL-002,REL-003,REL-007,EFF-001,EFF-002,EFF-003,SEC-003,SEC-004,SEC-006,SEC-009,SEC-010",
	}
	for id, w := range want {
		if g := failedIDs(got[id]); g != w {
			t.Errorf("%s\n got  %s\n want %s", id, g, w)
		}
	}
	for _, res := range got["StatefulSet/readiness-bad/cache"].Results {
		if res.ID == "SEC-005" {
			t.Error("SEC-005 is ignored by annotation on the cache")
		}
	}
	if rep.Grade != "F" || rep.Counts[Danger] == 0 {
		t.Errorf("score %d grade %s counts %v", rep.Score, rep.Grade, rep.Counts)
	}
	if rep.Workloads[0].Score > rep.Workloads[len(rep.Workloads)-1].Score {
		t.Error("workloads should be sorted worst first")
	}
	failed := got["DaemonSet/readiness-bad/node-agent"].Failed()
	if failed[0].Severity != Danger {
		t.Error("Failed() should list dangers first")
	}
}

func TestConfigOverridesAndExemptions(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
checks:
  SEC-005: ignore
  REL-006: danger
exemptions:
  - namespaces: ["readiness-*"]
    kinds: [DaemonSet]
    checks: [SEC-001, SEC-002, SEC-007, SEC-008]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := byID(Evaluate(load(t, "bad.yaml"), cfg))
	ds := failedIDs(got["DaemonSet/readiness-bad/node-agent"])
	for _, id := range []string{"SEC-001", "SEC-002", "SEC-005", "SEC-007", "SEC-008"} {
		if strings.Contains(ds, id) {
			t.Errorf("%s should be exempt or ignored, failed: %s", id, ds)
		}
	}
	for _, res := range got["Deployment/readiness-bad/api"].Results {
		if res.ID == "REL-006" && res.Severity != Danger {
			t.Error("severity override not applied")
		}
	}
}

func TestParseConfigErrors(t *testing.T) {
	for _, bad := range []string{
		"checks: {NOPE-1: warning}",
		"checks: {SEC-001: fatal}",
		"exemptions: [{checks: [NOPE-1]}]",
		"unknownField: true",
	} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestFloatingTag(t *testing.T) {
	cases := map[string]bool{
		"nginx":                         true,
		"nginx:latest":                  true,
		"registry:5000/team/app":        true,
		"registry:5000/team/app:1.2.3":  false,
		"nginx:1.27":                    false,
		"nginx@sha256:0123456789abcdef": false,
		"ghcr.io/org/app:latest":        true,
	}
	for img, want := range cases {
		if got := floatingTag(img); got != want {
			t.Errorf("floatingTag(%q) = %v", img, got)
		}
	}
}

func TestBlocksAll(t *testing.T) {
	i := func(v int32) *intstr.IntOrString { x := intstr.FromInt32(v); return &x }
	s := func(v string) *intstr.IntOrString { x := intstr.FromString(v); return &x }
	cases := []struct {
		maxU, minA *intstr.IntOrString
		replicas   int32
		want       bool
	}{
		{i(0), nil, 3, true},
		{i(1), nil, 3, false},
		{s("0%"), nil, 3, true},
		{s("10%"), nil, 3, false}, // rounds up to 1
		{nil, i(3), 3, true},
		{nil, i(2), 3, false},
		{nil, s("100%"), 3, true},
		{nil, s("50%"), 3, false},
		{nil, nil, 3, false},
	}
	for _, c := range cases {
		if got := blocksAll(c.maxU, c.minA, c.replicas); got != c.want {
			t.Errorf("max=%v min=%v replicas=%d: got %v", c.maxU, c.minA, c.replicas, got)
		}
	}
}

func TestWildcard(t *testing.T) {
	if !wildcard("kube-*", "kube-system") || wildcard("kube-*", "default") || !wildcard("*", "x") || !wildcard("a", "a") {
		t.Fatal("wildcard")
	}
}
