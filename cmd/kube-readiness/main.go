// Command kube-readiness scores Kubernetes workloads for production
// readiness — reliability, efficiency and security — from manifests (files,
// directories, `helm template` output) or from a live cluster.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/OTA-EITA/kube-readiness/internal/checks"
	"github.com/OTA-EITA/kube-readiness/internal/report"
	"github.com/OTA-EITA/kube-readiness/internal/workload"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

var version = "dev"

// newClient is replaced in tests.
var newClient = func(kubeconfig, kubeContext string) (kubernetes.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	cfg.UserAgent = "kube-readiness/" + version
	return kubernetes.NewForConfig(cfg)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("kube-readiness", flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.Usage = func() {
		fmt.Fprintln(stderr, "usage: kube-readiness [flags] FILE|DIR|- ...")
		fmt.Fprintln(stderr, "       kube-readiness --cluster [flags]")
		fset.PrintDefaults()
	}
	cluster := fset.Bool("cluster", false, "read workloads from the current kubeconfig context instead of files")
	kubeconfig := fset.String("kubeconfig", "", "path to kubeconfig (with --cluster)")
	kubeContext := fset.String("context", "", "kubeconfig context (with --cluster)")
	namespace := fset.String("namespace", "", "only this namespace (with --cluster; default all)")
	exclude := fset.String("exclude-namespace", "kube-system,kube-public,kube-node-lease", "comma separated namespaces to skip")
	configPath := fset.String("config", "", "YAML config with severity overrides and exemptions")
	output := fset.String("output", "table", "output format: table, json, markdown or sarif")
	minScore := fset.Int("min-score", 0, "exit 1 when the overall score is below this (0-100)")
	failOnDanger := fset.Bool("fail-on-danger", false, "exit 1 when any danger check fails")
	list := fset.Bool("list", false, "list checks and exit")
	showVersion := fset.Bool("version", false, "print version and exit")
	if err := fset.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if *showVersion {
		fmt.Fprintln(stdout, "kube-readiness", version)
		return exitOK
	}
	if *list {
		return listChecks(stdout)
	}
	format, err := report.ParseFormat(*output)
	if err != nil {
		return usageErr(stderr, err)
	}
	if *minScore < 0 || *minScore > 100 {
		return usageErr(stderr, fmt.Errorf("--min-score must be between 0 and 100"))
	}
	cfg := checks.Config{}
	if *configPath != "" {
		data, err := os.ReadFile(*configPath) // #nosec G304 -- config path chosen by the operator
		if err != nil {
			return usageErr(stderr, err)
		}
		if cfg, err = checks.ParseConfig(data); err != nil {
			return usageErr(stderr, err)
		}
	}

	var set workload.Set
	switch {
	case *cluster && fset.NArg() > 0:
		return usageErr(stderr, errors.New("--cluster cannot be combined with files"))
	case *cluster:
		cs, err := newClient(*kubeconfig, *kubeContext)
		if err != nil {
			return usageErr(stderr, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if set, err = workload.LoadCluster(ctx, cs, *namespace); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitFail
		}
	case fset.NArg() == 0:
		fset.Usage()
		return exitUsage
	default:
		if set, err = loadPaths(fset.Args(), stdin); err != nil {
			return usageErr(stderr, err)
		}
	}
	set = withoutNamespaces(set, *exclude)

	rep := checks.Evaluate(set, cfg)
	if err := report.Write(stdout, rep, format, version); err != nil {
		return usageErr(stderr, err)
	}
	if rep.Score < *minScore || (*failOnDanger && rep.Counts[checks.Danger] > 0) {
		return exitFail
	}
	return exitOK
}

func usageErr(w io.Writer, err error) int {
	fmt.Fprintln(w, "error:", err)
	return exitUsage
}

// loadPaths reads files, recursing into directories for *.yaml and *.yml.
func loadPaths(paths []string, stdin io.Reader) (workload.Set, error) {
	var all workload.Set
	add := func(data []byte, name string) error {
		s, err := workload.LoadManifests(data, name)
		if err != nil {
			return err
		}
		all.Workloads = append(all.Workloads, s.Workloads...)
		all.PDBs = append(all.PDBs, s.PDBs...)
		return nil
	}
	for _, p := range paths {
		if p == "-" {
			data, err := io.ReadAll(stdin)
			if err != nil {
				return all, err
			}
			if err := add(data, "stdin"); err != nil {
				return all, err
			}
			continue
		}
		err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if path != p && !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
				return nil
			}
			data, err := os.ReadFile(path) // #nosec G304 -- walking user-specified manifests is the purpose of the tool
			if err != nil {
				return err
			}
			return add(data, filepath.ToSlash(path))
		})
		if err != nil {
			return all, err
		}
	}
	return all, nil
}

func withoutNamespaces(set workload.Set, csv string) workload.Set {
	skip := map[string]bool{}
	for _, ns := range strings.Split(csv, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			skip[ns] = true
		}
	}
	out := workload.Set{PDBs: set.PDBs}
	for _, w := range set.Workloads {
		if !skip[w.Namespace] {
			out.Workloads = append(out.Workloads, w)
		}
	}
	return out
}

func listChecks(w io.Writer) int {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHECK\tCATEGORY\tSEVERITY\tTITLE")
	for _, c := range checks.All() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.ID, c.Category, c.Severity, c.Title)
	}
	if tw.Flush() != nil {
		return exitUsage
	}
	return exitOK
}
