package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
  deadvalues scan apps --changed-since origin/main`,
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

			keep := func(name string) bool {
				return (len(only) == 0 || slices.Contains(only, name)) && !slices.Contains(skip, name)
			}
			jobs, reports, err := g.dirJobs(dir, root, keep)
			if err != nil {
				return &exitError{code: 2, err: err}
			}
			for _, j := range jobs {
				if changed != nil && !affected(j.appFile, []job{j}, changed) {
					continue
				}
				reports = append(reports, g.check(cmd.Context(), j))
			}

			if len(reports) == 0 {
				fmt.Fprintln(g.stderr, "no Argo CD Applications or Flux HelmReleases with Helm charts to scan")
				if g.output == "table" && len(g.outputs) == 0 {
					return nil
				}
			}
			report.ResolveShared(reports)
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
	cmd.Flags().StringSliceVar(&only, "only", nil, "scan only these Applications or HelmReleases (by metadata.name)")
	cmd.Flags().StringSliceVar(&skip, "skip", nil, "skip these Applications or HelmReleases")
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
		for _, src := range j.vals.Sources {
			if changed[normPath(src)] {
				return true
			}
		}
		if j.ref.IsLocal() {
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
