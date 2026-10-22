package workload

import (
	"os"
	"strings"
	"testing"
)

func TestLoadManifests(t *testing.T) {
	data, err := os.ReadFile("../../examples/bad.yaml")
	if err != nil {
		t.Fatal(err)
	}
	set, err := LoadManifests(data, "bad.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Workloads) != 3 || len(set.PDBs) != 1 {
		t.Fatalf("got %d workloads, %d pdbs", len(set.Workloads), len(set.PDBs))
	}
	api := set.Workloads[0]
	if api.ID() != "Deployment/readiness-bad/api" || *api.Replicas != 2 || api.Source.File != "bad.yaml" || api.Source.Line != 8 {
		t.Fatalf("api: %+v", api)
	}
	if len(set.PDBsFor(api)) != 1 {
		t.Fatal("the api PDB should select the api pods")
	}
	if ds := set.Workloads[1]; ds.Replicas != nil || ds.Kind != "DaemonSet" {
		t.Fatalf("daemonset: %+v", ds)
	}
	if len(set.PDBsFor(set.Workloads[2])) != 0 {
		t.Fatal("no PDB selects the cache")
	}
}

func TestLoadListAndDefaults(t *testing.T) {
	list := `
apiVersion: v1
kind: List
items:
  - apiVersion: batch/v1
    kind: CronJob
    metadata: {name: report}
    spec:
      schedule: "0 * * * *"
      jobTemplate:
        spec:
          template:
            spec:
              containers: [{name: r, image: busybox:1.36}]
  - apiVersion: v1
    kind: Service
    metadata: {name: ignored}
  - apiVersion: v1
    kind: Pod
    metadata: {name: debug, namespace: ops}
    spec:
      containers: [{name: d, image: busybox}]
---
---
just-a-scalar
`
	set, err := LoadManifests([]byte(list), "list.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Workloads) != 2 {
		t.Fatalf("got %+v", set.Workloads)
	}
	cj := set.Workloads[0]
	if cj.Namespace != "default" || !cj.RunsToCompletion() || cj.Template.Spec.Containers[0].Image != "busybox:1.36" {
		t.Fatalf("cronjob: %+v", cj)
	}
	if set.Workloads[1].ID() != "Pod/ops/debug" {
		t.Fatalf("pod: %+v", set.Workloads[1])
	}
}

func TestLoadManifestsErrors(t *testing.T) {
	_, err := LoadManifests([]byte("kind: Deployment\nspec:\n  replicas: many\n"), "x.yaml")
	if err == nil || !strings.Contains(err.Error(), "x.yaml:1") {
		t.Fatalf("got %v", err)
	}
	if _, err := LoadManifests([]byte("a: [\n"), "y.yaml"); err == nil {
		t.Fatal("invalid YAML must fail")
	}
}
