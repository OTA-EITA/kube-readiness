package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OTA-EITA/kube-readiness/internal/checks"
	"github.com/OTA-EITA/kube-readiness/internal/workload"
)

func sample(t *testing.T) checks.Report {
	t.Helper()
	set, err := workload.LoadManifests([]byte(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: app}
spec:
  replicas: 1
  template:
    metadata: {labels: {app: api}}
    spec:
      containers: [{name: api, image: "nginx:latest"}]
`), "deploy/api.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return checks.Evaluate(set, checks.Config{})
}

func TestTable(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, sample(t), Table, "test"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Deployment/app/api", "REL-007!", "Score ", "1 workloads"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q:\n%s", want, b.String())
		}
	}
}

func TestMarkdown(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, sample(t), Markdown, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "## kube-readiness:") || !strings.Contains(b.String(), "REL-007 Image uses a fixed tag") {
		t.Errorf("markdown:\n%s", b.String())
	}
}

func TestJSON(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, sample(t), JSON, "test"); err != nil {
		t.Fatal(err)
	}
	var got checks.Report
	if err := json.Unmarshal(b.Bytes(), &got); err != nil || len(got.Workloads) != 1 {
		t.Fatalf("%v %s", err, b.String())
	}
}

func TestSARIFPointsAtManifestLine(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, sample(t), SARIF, "v1"); err != nil {
		t.Fatal(err)
	}
	var log struct {
		Version string
		Runs    []struct {
			Results []struct {
				RuleID    string
				Level     string
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct{ URI string }
						Region           struct{ StartLine int }
					}
				}
			}
		}
	}
	if err := json.Unmarshal(b.Bytes(), &log); err != nil {
		t.Fatal(err)
	}
	if log.Version != "2.1.0" || len(log.Runs[0].Results) == 0 {
		t.Fatalf("%s", b.String())
	}
	for _, r := range log.Runs[0].Results {
		loc := r.Locations[0].PhysicalLocation
		if loc.ArtifactLocation.URI != "deploy/api.yaml" || loc.Region.StartLine != 2 {
			t.Errorf("%s: %+v", r.RuleID, loc)
		}
		if r.RuleID == "REL-007" && r.Level != "error" {
			t.Errorf("danger checks are errors")
		}
	}
	if _, err := ParseFormat("html"); err == nil {
		t.Error("unknown formats must be rejected")
	}
}
