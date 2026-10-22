// Package workload normalises Kubernetes workloads from manifests or a live
// cluster into one shape that checks can inspect.
package workload

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Source locates an object in a manifest file. Line is 1-based; both are
// empty for objects read from a cluster.
type Source struct {
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
}

// Workload is anything that runs pods.
type Workload struct {
	Kind        string            `json:"kind"`
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Annotations map[string]string `json:"-"`
	// Replicas is nil for kinds without a replica count (DaemonSet, Job, Pod).
	Replicas *int32                 `json:"-"`
	Template corev1.PodTemplateSpec `json:"-"`
	Source   Source                 `json:"source"`
}

// ID is a stable human readable identifier such as Deployment/app/api.
func (w Workload) ID() string { return w.Kind + "/" + w.Namespace + "/" + w.Name }

// RunsToCompletion reports Jobs and CronJobs, which do not serve traffic.
func (w Workload) RunsToCompletion() bool { return w.Kind == "Job" || w.Kind == "CronJob" }

// Set is everything that was loaded.
type Set struct {
	Workloads []Workload
	PDBs      []policyv1.PodDisruptionBudget
}

// PDBsFor returns the budgets whose selector matches the workload's pods.
func (s Set) PDBsFor(w Workload) []policyv1.PodDisruptionBudget {
	var out []policyv1.PodDisruptionBudget
	for _, p := range s.PDBs {
		if p.Namespace != w.Namespace || p.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil {
			continue
		}
		if sel.Matches(labels.Set(w.Template.Labels)) {
			out = append(out, p)
		}
	}
	return out
}

func fromDeployment(d appsv1.Deployment) Workload {
	r := d.Spec.Replicas
	if r == nil {
		one := int32(1)
		r = &one
	}
	return Workload{Kind: "Deployment", Namespace: d.Namespace, Name: d.Name, Annotations: d.Annotations, Replicas: r, Template: d.Spec.Template}
}

func fromStatefulSet(s appsv1.StatefulSet) Workload {
	r := s.Spec.Replicas
	if r == nil {
		one := int32(1)
		r = &one
	}
	return Workload{Kind: "StatefulSet", Namespace: s.Namespace, Name: s.Name, Annotations: s.Annotations, Replicas: r, Template: s.Spec.Template}
}

func fromDaemonSet(d appsv1.DaemonSet) Workload {
	return Workload{Kind: "DaemonSet", Namespace: d.Namespace, Name: d.Name, Annotations: d.Annotations, Template: d.Spec.Template}
}

func fromJob(j batchv1.Job) Workload {
	return Workload{Kind: "Job", Namespace: j.Namespace, Name: j.Name, Annotations: j.Annotations, Template: j.Spec.Template}
}

func fromCronJob(c batchv1.CronJob) Workload {
	return Workload{Kind: "CronJob", Namespace: c.Namespace, Name: c.Name, Annotations: c.Annotations, Template: c.Spec.JobTemplate.Spec.Template}
}

func fromPod(p corev1.Pod) Workload {
	return Workload{Kind: "Pod", Namespace: p.Namespace, Name: p.Name, Annotations: p.Annotations,
		Template: corev1.PodTemplateSpec{ObjectMeta: p.ObjectMeta, Spec: p.Spec}}
}
