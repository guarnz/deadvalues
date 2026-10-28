package cli

import (
	"os"
	"path/filepath"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/source"
	"github.com/guarnz/deadvalues/internal/values"
)

// valuesJobs handles -f given without --app or --chart: it finds what loads
// those values files. First every Application and HelmRelease of the
// repository that uses them (all of them, so a shared file is judged against
// every app), else the chart directory that contains the file (a chart's
// ci/ values, for instance). Findings are restricted to keys from the files.
func (g *globals) valuesJobs(t *targetFlags) ([]job, error) {
	only := map[string]bool{}
	for _, f := range t.values {
		if _, err := os.Stat(f); err != nil {
			return nil, &exitError{code: 2, err: err}
		}
		only[normPath(f)] = true
	}
	root := t.repoRoot
	if root == "" {
		root = argo.RepoRoot(t.values[0])
	}

	all, _, err := g.dirJobs(root, root, nil)
	if err != nil {
		return nil, &exitError{code: 2, err: err}
	}
	var out []job
	for _, j := range all {
		uses := false
		for _, s := range j.vals.Sources {
			uses = uses || only[normPath(s)]
		}
		if !uses {
			continue
		}
		for _, s := range t.set {
			if err := j.vals.AddSet(s, "--set", false); err != nil {
				return nil, &exitError{code: 2, err: err}
			}
		}
		if t.release != "" {
			j.release = t.release
		}
		if t.namespace != "" {
			j.namespace = t.namespace
		}
		j.only = only
		out = append(out, j)
	}
	if len(out) > 0 {
		return out, nil
	}

	for _, f := range t.values {
		dir, ok := enclosingChart(f, root)
		if !ok {
			continue
		}
		set := values.New()
		if err := addExtra(set, t); err != nil {
			return nil, err
		}
		name := t.release
		if name == "" {
			name = chartName(dir)
		}
		return []job{{
			name:      name,
			app:       name,
			ref:       source.Ref{Chart: dir},
			release:   t.release,
			namespace: t.namespace,
			vals:      set,
			only:      only,
		}}, nil
	}
	return nil, usageErr("%s: no Argo CD Application, Flux HelmRelease or chart directory in %s uses this file; pass --chart or --app", t.values[0], root)
}

// enclosingChart finds the chart directory a values file lives in: its own
// directory or a parent, up to the repository root.
func enclosingChart(file, root string) (string, bool) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", false
	}
	stop := normPath(root)
	for dir := filepath.Dir(abs); ; {
		if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if normPath(dir) == stop || parent == dir {
			return "", false
		}
		dir = parent
	}
}
