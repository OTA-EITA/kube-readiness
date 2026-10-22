package workload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
)

// LoadManifests parses multi-document YAML, including `kind: List` documents
// produced by `kubectl get -o yaml`, and records where each object starts.
// Unrelated kinds (Service, ConfigMap, CRDs, ...) are ignored.
func LoadManifests(data []byte, file string) (Set, error) {
	var set Set
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return set, nil
		}
		if err != nil {
			return set, fmt.Errorf("%s: %w", file, err)
		}
		if len(doc.Content) == 0 {
			continue
		}
		if err := addNode(&set, doc.Content[0], file); err != nil {
			return set, err
		}
	}
}

func addNode(set *Set, n *yaml.Node, file string) error {
	var head struct {
		Kind string `yaml:"kind"`
	}
	if err := n.Decode(&head); err != nil {
		return nil // not a mapping, e.g. a stray scalar
	}
	if head.Kind == "List" {
		var list struct {
			Items []yaml.Node `yaml:"items"`
		}
		if err := n.Decode(&list); err != nil {
			return fmt.Errorf("%s:%d: %w", file, n.Line, err)
		}
		for i := range list.Items {
			if err := addNode(set, &list.Items[i], file); err != nil {
				return err
			}
		}
		return nil
	}
	src := Source{File: file, Line: n.Line}
	var generic any
	if err := n.Decode(&generic); err != nil {
		return fmt.Errorf("%s:%d: %w", file, n.Line, err)
	}
	js, err := json.Marshal(generic)
	if err != nil {
		return fmt.Errorf("%s:%d: %w", file, n.Line, err)
	}
	decode := func(v any) error {
		if err := json.Unmarshal(js, v); err != nil {
			return fmt.Errorf("%s:%d: %s: %w", file, n.Line, head.Kind, err)
		}
		return nil
	}
	var w Workload
	switch head.Kind {
	case "Deployment":
		var o appsv1.Deployment
		if err := decode(&o); err != nil {
			return err
		}
		w = fromDeployment(o)
	case "StatefulSet":
		var o appsv1.StatefulSet
		if err := decode(&o); err != nil {
			return err
		}
		w = fromStatefulSet(o)
	case "DaemonSet":
		var o appsv1.DaemonSet
		if err := decode(&o); err != nil {
			return err
		}
		w = fromDaemonSet(o)
	case "Job":
		var o batchv1.Job
		if err := decode(&o); err != nil {
			return err
		}
		w = fromJob(o)
	case "CronJob":
		var o batchv1.CronJob
		if err := decode(&o); err != nil {
			return err
		}
		w = fromCronJob(o)
	case "Pod":
		var o corev1.Pod
		if err := decode(&o); err != nil {
			return err
		}
		w = fromPod(o)
	case "PodDisruptionBudget":
		var o policyv1.PodDisruptionBudget
		if err := decode(&o); err != nil {
			return err
		}
		if o.Namespace == "" {
			o.Namespace = "default"
		}
		set.PDBs = append(set.PDBs, o)
		return nil
	default:
		return nil
	}
	if w.Namespace == "" {
		w.Namespace = "default"
	}
	w.Source = src
	set.Workloads = append(set.Workloads, w)
	return nil
}
