package workload

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// LoadCluster lists workloads from a live cluster. Pods, ReplicaSets and Jobs
// that are owned by another object are skipped, because their owner is
// checked instead.
func LoadCluster(ctx context.Context, cs kubernetes.Interface, namespace string) (Set, error) {
	var set Set
	opts := metav1.ListOptions{}

	deps, err := cs.AppsV1().Deployments(namespace).List(ctx, opts)
	if err != nil {
		return set, fmt.Errorf("list deployments: %w", err)
	}
	for _, o := range deps.Items {
		set.Workloads = append(set.Workloads, fromDeployment(o))
	}
	sts, err := cs.AppsV1().StatefulSets(namespace).List(ctx, opts)
	if err != nil {
		return set, fmt.Errorf("list statefulsets: %w", err)
	}
	for _, o := range sts.Items {
		set.Workloads = append(set.Workloads, fromStatefulSet(o))
	}
	dss, err := cs.AppsV1().DaemonSets(namespace).List(ctx, opts)
	if err != nil {
		return set, fmt.Errorf("list daemonsets: %w", err)
	}
	for _, o := range dss.Items {
		set.Workloads = append(set.Workloads, fromDaemonSet(o))
	}
	cjs, err := cs.BatchV1().CronJobs(namespace).List(ctx, opts)
	if err != nil {
		return set, fmt.Errorf("list cronjobs: %w", err)
	}
	for _, o := range cjs.Items {
		set.Workloads = append(set.Workloads, fromCronJob(o))
	}
	jobs, err := cs.BatchV1().Jobs(namespace).List(ctx, opts)
	if err != nil {
		return set, fmt.Errorf("list jobs: %w", err)
	}
	for _, o := range jobs.Items {
		if metav1.GetControllerOf(&o) == nil {
			set.Workloads = append(set.Workloads, fromJob(o))
		}
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, opts)
	if err != nil {
		return set, fmt.Errorf("list pods: %w", err)
	}
	for _, o := range pods.Items {
		if metav1.GetControllerOf(&o) == nil && o.Annotations["kubernetes.io/config.mirror"] == "" {
			set.Workloads = append(set.Workloads, fromPod(o))
		}
	}
	pdbs, err := cs.PolicyV1().PodDisruptionBudgets(namespace).List(ctx, opts)
	if err != nil {
		return set, fmt.Errorf("list poddisruptionbudgets: %w", err)
	}
	set.PDBs = pdbs.Items
	return set, nil
}
