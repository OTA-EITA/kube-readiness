// Package checks scores workloads against production readiness checks for
// reliability, efficiency and security.
package checks

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/OTA-EITA/kube-readiness/internal/workload"
)

// Severity of a failed check.
type Severity string

// Severities. Danger is weighted three times a warning in the score.
const (
	Danger  Severity = "danger"
	Warning Severity = "warning"
	Ignore  Severity = "ignore"
)

// Weight returns how much a check counts towards the score.
func (s Severity) Weight() int {
	switch s {
	case Danger:
		return 3
	case Warning:
		return 1
	}
	return 0
}

// Category groups checks.
type Category string

// Categories.
const (
	Reliability Category = "reliability"
	Efficiency  Category = "efficiency"
	Security    Category = "security"
)

// Check is one readiness rule.
type Check struct {
	ID       string
	Category Category
	Severity Severity
	Title    string
	// Applies limits a check to some workloads; nil means all.
	Applies func(w workload.Workload) bool
	// Eval returns "" when the workload passes, or a description of the problem.
	Eval func(w workload.Workload, s workload.Set) string
}

func serving(w workload.Workload) bool { return !w.RunsToCompletion() }

func replicated(w workload.Workload) bool { return w.Replicas != nil && *w.Replicas >= 2 }

// containers returns app and init containers (init containers matter for security).
func containers(w workload.Workload, includeInit bool) []corev1.Container {
	out := append([]corev1.Container{}, w.Template.Spec.Containers...)
	if includeInit {
		out = append(out, w.Template.Spec.InitContainers...)
	}
	return out
}

// eachContainer fails with the names of containers where bad returns true.
func eachContainer(includeInit bool, msg string, bad func(w workload.Workload, c corev1.Container) bool) func(workload.Workload, workload.Set) string {
	return func(w workload.Workload, _ workload.Set) string {
		var names []string
		for _, c := range containers(w, includeInit) {
			if bad(w, c) {
				names = append(names, c.Name)
			}
		}
		if len(names) == 0 {
			return ""
		}
		return msg + ": " + strings.Join(names, ", ")
	}
}

func effectiveSC(w workload.Workload, c corev1.Container) (podSC *corev1.PodSecurityContext, sc *corev1.SecurityContext) {
	podSC = w.Template.Spec.SecurityContext
	if podSC == nil {
		podSC = &corev1.PodSecurityContext{}
	}
	sc = c.SecurityContext
	if sc == nil {
		sc = &corev1.SecurityContext{}
	}
	return podSC, sc
}

func isTrue(b *bool) bool  { return b != nil && *b }
func isFalse(b *bool) bool { return b != nil && !*b }

var dangerousCaps = map[corev1.Capability]bool{
	"ALL": true, "SYS_ADMIN": true, "NET_ADMIN": true, "SYS_PTRACE": true, "SYS_MODULE": true, "NET_RAW": true, "BPF": true,
}

// All returns every check with its default severity.
func All() []Check {
	return []Check{
		// Reliability
		{ID: "REL-001", Category: Reliability, Severity: Warning, Title: "Readiness probe is configured", Applies: serving,
			Eval: eachContainer(false, "no readinessProbe", func(_ workload.Workload, c corev1.Container) bool { return c.ReadinessProbe == nil })},
		{ID: "REL-002", Category: Reliability, Severity: Warning, Title: "Liveness probe is configured", Applies: serving,
			Eval: eachContainer(false, "no livenessProbe", func(_ workload.Workload, c corev1.Container) bool { return c.LivenessProbe == nil })},
		{ID: "REL-003", Category: Reliability, Severity: Warning, Title: "More than one replica",
			Applies: func(w workload.Workload) bool { return w.Replicas != nil },
			Eval: func(w workload.Workload, _ workload.Set) string {
				if *w.Replicas < 2 && *w.Replicas != 0 {
					return fmt.Sprintf("replicas is %d; a node drain or crash causes downtime", *w.Replicas)
				}
				return ""
			}},
		{ID: "REL-004", Category: Reliability, Severity: Warning, Title: "PodDisruptionBudget protects the workload", Applies: replicated,
			Eval: func(w workload.Workload, s workload.Set) string {
				if len(s.PDBsFor(w)) == 0 {
					return "no PodDisruptionBudget selects these pods; voluntary disruptions can take them all down"
				}
				return ""
			}},
		{ID: "REL-005", Category: Reliability, Severity: Danger, Title: "PodDisruptionBudget allows at least one disruption", Applies: replicated,
			Eval: func(w workload.Workload, s workload.Set) string {
				for _, p := range s.PDBsFor(w) {
					if blocksAll(p.Spec.MaxUnavailable, p.Spec.MinAvailable, *w.Replicas) {
						return "PodDisruptionBudget " + p.Name + " allows no disruptions; node drains and upgrades will hang"
					}
				}
				return ""
			}},
		{ID: "REL-006", Category: Reliability, Severity: Warning, Title: "Replicas are spread across nodes", Applies: replicated,
			Eval: func(w workload.Workload, _ workload.Set) string {
				spec := w.Template.Spec
				if len(spec.TopologySpreadConstraints) > 0 {
					return ""
				}
				if a := spec.Affinity; a != nil && a.PodAntiAffinity != nil &&
					(len(a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 || len(a.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution) > 0) {
					return ""
				}
				return "no topologySpreadConstraints or podAntiAffinity; all replicas may land on one node"
			}},
		{ID: "REL-007", Category: Reliability, Severity: Danger, Title: "Image uses a fixed tag or digest",
			Eval: eachContainer(true, "image tag is latest or missing", func(_ workload.Workload, c corev1.Container) bool { return floatingTag(c.Image) })},

		// Efficiency
		{ID: "EFF-001", Category: Efficiency, Severity: Warning, Title: "CPU request is set",
			Eval: eachContainer(false, "no cpu request", func(_ workload.Workload, c corev1.Container) bool {
				_, ok := c.Resources.Requests[corev1.ResourceCPU]
				return !ok
			})},
		{ID: "EFF-002", Category: Efficiency, Severity: Warning, Title: "Memory request is set",
			Eval: eachContainer(false, "no memory request", func(_ workload.Workload, c corev1.Container) bool {
				_, ok := c.Resources.Requests[corev1.ResourceMemory]
				return !ok
			})},
		{ID: "EFF-003", Category: Efficiency, Severity: Warning, Title: "Memory limit is set",
			Eval: eachContainer(false, "no memory limit", func(_ workload.Workload, c corev1.Container) bool {
				_, ok := c.Resources.Limits[corev1.ResourceMemory]
				return !ok
			})},

		// Security
		{ID: "SEC-001", Category: Security, Severity: Danger, Title: "Container is not privileged",
			Eval: eachContainer(true, "privileged", func(w workload.Workload, c corev1.Container) bool {
				_, sc := effectiveSC(w, c)
				return isTrue(sc.Privileged)
			})},
		{ID: "SEC-002", Category: Security, Severity: Danger, Title: "Host namespaces are not shared",
			Eval: func(w workload.Workload, _ workload.Set) string {
				var used []string
				if w.Template.Spec.HostNetwork {
					used = append(used, "hostNetwork")
				}
				if w.Template.Spec.HostPID {
					used = append(used, "hostPID")
				}
				if w.Template.Spec.HostIPC {
					used = append(used, "hostIPC")
				}
				if len(used) == 0 {
					return ""
				}
				return "uses " + strings.Join(used, ", ")
			}},
		{ID: "SEC-003", Category: Security, Severity: Warning, Title: "Runs as non-root",
			Eval: eachContainer(true, "may run as root", func(w workload.Workload, c corev1.Container) bool {
				pod, sc := effectiveSC(w, c)
				nonRoot := isTrue(pod.RunAsNonRoot)
				if sc.RunAsNonRoot != nil {
					nonRoot = *sc.RunAsNonRoot
				}
				uid := pod.RunAsUser
				if sc.RunAsUser != nil {
					uid = sc.RunAsUser
				}
				if uid != nil {
					return *uid == 0
				}
				return !nonRoot
			})},
		{ID: "SEC-004", Category: Security, Severity: Warning, Title: "Privilege escalation is disabled",
			Eval: eachContainer(true, "allowPrivilegeEscalation is not false", func(w workload.Workload, c corev1.Container) bool {
				_, sc := effectiveSC(w, c)
				return !isFalse(sc.AllowPrivilegeEscalation)
			})},
		{ID: "SEC-005", Category: Security, Severity: Warning, Title: "Root filesystem is read-only",
			Eval: eachContainer(false, "readOnlyRootFilesystem is not true", func(w workload.Workload, c corev1.Container) bool {
				_, sc := effectiveSC(w, c)
				return !isTrue(sc.ReadOnlyRootFilesystem)
			})},
		{ID: "SEC-006", Category: Security, Severity: Warning, Title: "All capabilities are dropped",
			Eval: eachContainer(true, "capabilities.drop does not include ALL", func(w workload.Workload, c corev1.Container) bool {
				_, sc := effectiveSC(w, c)
				if sc.Capabilities == nil {
					return true
				}
				for _, d := range sc.Capabilities.Drop {
					if d == "ALL" {
						return false
					}
				}
				return true
			})},
		{ID: "SEC-007", Category: Security, Severity: Danger, Title: "No dangerous capabilities are added",
			Eval: eachContainer(true, "adds SYS_ADMIN, NET_ADMIN, NET_RAW or similar", func(w workload.Workload, c corev1.Container) bool {
				_, sc := effectiveSC(w, c)
				if sc.Capabilities == nil {
					return false
				}
				for _, a := range sc.Capabilities.Add {
					if dangerousCaps[a] {
						return true
					}
				}
				return false
			})},
		{ID: "SEC-008", Category: Security, Severity: Warning, Title: "No hostPath volumes",
			Eval: func(w workload.Workload, _ workload.Set) string {
				var names []string
				for _, v := range w.Template.Spec.Volumes {
					if v.HostPath != nil {
						names = append(names, v.Name+" ("+v.HostPath.Path+")")
					}
				}
				if len(names) == 0 {
					return ""
				}
				return "mounts hostPath: " + strings.Join(names, ", ")
			}},
		{ID: "SEC-009", Category: Security, Severity: Warning, Title: "Seccomp profile is RuntimeDefault or Localhost",
			Eval: eachContainer(true, "no RuntimeDefault seccomp profile", func(w workload.Workload, c corev1.Container) bool {
				pod, sc := effectiveSC(w, c)
				p := pod.SeccompProfile
				if sc.SeccompProfile != nil {
					p = sc.SeccompProfile
				}
				return p == nil || p.Type == corev1.SeccompProfileTypeUnconfined
			})},
		{ID: "SEC-010", Category: Security, Severity: Warning, Title: "Service account token is not mounted automatically",
			Eval: func(w workload.Workload, _ workload.Set) string {
				if isFalse(w.Template.Spec.AutomountServiceAccountToken) {
					return ""
				}
				return "automountServiceAccountToken is not false; set it to false unless the pod calls the Kubernetes API"
			}},
	}
}

// floatingTag reports images that are untagged or tagged latest.
func floatingTag(image string) bool {
	if strings.Contains(image, "@sha256:") {
		return false
	}
	name := image
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	tag := ""
	if i := strings.LastIndex(name, ":"); i >= 0 {
		tag = name[i+1:]
	}
	return tag == "" || tag == "latest"
}

// blocksAll reports a budget that can never allow an eviction. Percentages
// are rounded up for both fields, as the disruption controller does.
func blocksAll(maxUnavailable, minAvailable *intstr.IntOrString, replicas int32) bool {
	switch {
	case maxUnavailable != nil:
		v, err := intstr.GetScaledValueFromIntOrPercent(maxUnavailable, int(replicas), true)
		return err == nil && v == 0
	case minAvailable != nil:
		v, err := intstr.GetScaledValueFromIntOrPercent(minAvailable, int(replicas), true)
		return err == nil && v >= int(replicas)
	}
	return false
}
