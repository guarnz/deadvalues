package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/report"
)

func newScanCmd(g *globals) *cobra.Command {
	var (
		only, skip   []string
		changedSince string
		failOn       string
		details      bool
		repoRoot     string
	)
	cmd := &cobra.Command{
		Use:   "scan <dir>",
		Short: "Run check on every Argo CD Application and Flux HelmRelease in a directory",
		Long: `Run check on every Argo CD Application and Flux HelmRelease in a directory.

Directories with a kustomization.yaml are built first, the way Flux does, so
HelmReleases are checked with their overlay patches applied. Sources without
a Helm chart are skipped. One failing app does not stop the scan; it is
reported as ERROR in the summary.`,
		Example: `  deadvalues scan apps
  deadvalues scan apps --changed-since origin/main
  deadvalues scan apps --skip cilium,apps/legacy`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			level, err := g.failOn(cmd, failOn, "dead", checkLevels)
			if err != nil {
				return err
			}
			dir, err := filepath.Abs(args[0])
			if err != nil {
				return &exitError{code: 2, err: err}
			}
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				return usageErr("%s is not a directory", args[0])
			}
			root := repoRoot
			if root == "" {
				root = argo.RepoRoot(filepath.Join(dir, "x"))
			}

			var changed map[string]bool
			if changedSince != "" {
				raw, err := argo.ChangedFiles(root, changedSince)
				if err != nil {
					return &exitError{code: 2, err: err}
				}
				changed = map[string]bool{}
				for f := range raw {
					changed[normPath(f)] = true
				}
			}

			jobs, failed, err := g.dirJobs(dir, root, newAppFilter(only, skip))
			if err != nil {
				return &exitError{code: 2, err: err}
			}
			var base map[string]job
			if changed != nil {
				base = g.baseJobs(dir, root, changedSince, newAppFilter(only, skip))
			}
			var reports []*report.Check
			for _, c := range failed {
				if changed == nil || touches(c.AppFile, changed) {
					reports = append(reports, c)
				}
			}
			var picked, others []job
			for _, j := range jobs {
				if changed == nil || affected(j.appFile, []job{j}, changed) || inputChanged(j, base) {
					picked = append(picked, j)
				} else {
					others = append(others, j)
				}
			}
			g.crds = newCRDSearch(root)
			for _, j := range picked {
				reports = append(reports, g.check(cmd.Context(), j))
			}
			var neighbours []*report.Check
			for _, j := range sharingValues(others, picked) {
				neighbours = append(neighbours, g.check(cmd.Context(), j))
			}

			g.crds.report(g.stderr)
			if len(reports) == 0 {
				if changed != nil && len(jobs) > 0 {
					fmt.Fprintln(g.stderr, "no app changed since "+changedSince)
				} else {
					fmt.Fprintln(g.stderr, "no Argo CD Applications or Flux HelmReleases with Helm charts to scan")
				}
				if g.output == "table" && len(g.outputs) == 0 {
					return nil
				}
			}
			report.ResolveShared(append(slices.Clone(reports), neighbours...))
			err = g.emit(func(w *report.Writer) error {
				w.Details = details
				return w.Checks(reports, true)
			})
			if err != nil {
				return err
			}
			return checkExit(reports, level)
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "scan only these Applications or HelmReleases, by name, namespace/name or the name in the report")
	cmd.Flags().StringSliceVar(&skip, "skip", nil, "skip these Applications or HelmReleases (by name, namespace/name or the name in the report) or directories")
	cmd.Flags().StringVar(&changedSince, "changed-since", "", "scan only targets whose files changed since this git ref")
	cmd.Flags().StringVar(&failOn, "fail-on", "dead", "dead | conditional | redundant | none")
	cmd.Flags().BoolVar(&details, "details", false, "print per-key findings, not only the summary table")
	cmd.Flags().StringVar(&repoRoot, "repo-root", "", "repository root for $values/, sources and local charts (default: git top-level)")
	return cmd
}

func yamlFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(p); ext == ".yaml" || ext == ".yml" {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// affected reports whether an Application, one of its values files or its
// local chart changed.
func affected(appFile string, jobs []job, changed map[string]bool) bool {
	if changed[normPath(appFile)] {
		return true
	}
	for _, j := range jobs {
		for _, src := range append(append([]string{}, j.vals.Sources...), j.watch...) {
			if changed[normPath(src)] {
				return true
			}
		}
		if j.ref.IsLocal() && j.ref.Chart != "" {
			dir := normPath(j.ref.Chart) + string(filepath.Separator)
			for f := range changed {
				if strings.HasPrefix(f, dir) {
					return true
				}
			}
		}
	}
	return false
}

// baseJobs returns the targets as they were where the branch left rev, by ident. It is
// nil when that tree cannot be read, and then only the changed files select apps.
func (g *globals) baseJobs(dir, root, rev string, filter *appFilter) map[string]job {
	s, err := g.gitSides(root, argo.MergeBase(root, rev), "")
	if err != nil {
		return nil
	}
	defer s.close()
	baseDir := s.inBase(dir)
	if !dirExists(baseDir) {
		return nil
	}
	jobs, _, err := g.dirJobs(baseDir, s.baseRoot, filter.in(s.inBase))
	if err != nil {
		return nil
	}
	return byIdent(jobs)
}

// inputChanged reports whether what an app is analyzed with differs from the base,
// wherever that came from: an OCIRepository file, a variable, a parent's patch.
func inputChanged(j job, base map[string]job) bool {
	if base == nil || j.skip != "" {
		return false
	}
	o, ok := base[j.ident]
	if !ok || o.skip != "" {
		return true
	}
	if !j.ref.IsLocal() && j.ref != o.ref {
		return true
	}
	return j.release != o.release || j.namespace != o.namespace || !reflect.DeepEqual(j.vals.Values, o.vals.Values)
}

// touches reports whether a changed file is path or lies under it; a target that
// failed without a known file is always reported.
func touches(path string, changed map[string]bool) bool {
	if path == "" {
		return true
	}
	p := normPath(path)
	if changed[p] {
		return true
	}
	for f := range changed {
		if strings.HasPrefix(f, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// sharingValues returns the jobs among others that load a values file of
// picked: they only help judge the keys of shared files and are not reported.
func sharingValues(others, picked []job) []job {
	files := map[string]bool{}
	for _, j := range picked {
		for _, src := range j.vals.Sources {
			files[normPath(src)] = true
		}
	}
	var out []job
	for _, j := range others {
		for _, src := range j.vals.Sources {
			if files[normPath(src)] {
				out = append(out, j)
				break
			}
		}
	}
	return out
}

func normPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}
