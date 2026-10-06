package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/flux"
	"github.com/guarnz/deadvalues/internal/report"
	"github.com/guarnz/deadvalues/internal/source"
	"github.com/guarnz/deadvalues/internal/values"
)

// manifestJobs loads the file given to --app: an Argo CD Application or a
// Flux HelmRelease, detected by kind.
func (g *globals) manifestJobs(file string, t *targetFlags) ([]job, error) {
	objs, err := flux.ParseFile(file, g.cfg.Flux.Substitute)
	if err != nil {
		return nil, &exitError{code: 2, err: fmt.Errorf("%s: %w", file, err)}
	}
	root := t.repoRoot
	if root == "" {
		root = argo.RepoRoot(file)
	}
	hasApp, hasRelease := false, false
	for _, o := range objs {
		hasApp = hasApp || o.Kind == "Application"
		hasRelease = hasRelease || flux.IsRelease(o)
	}
	switch {
	case hasApp:
		app, err := argo.Load(file)
		if err != nil {
			return nil, &exitError{code: 2, err: err}
		}
		return g.appJobs(app, root, t)
	case hasRelease:
		jobs, failed := g.fluxFileJobs([]string{file}, root, t, nil)
		if len(failed) > 0 {
			return nil, &exitError{code: 2, err: errors.New(failed[0].Error)}
		}
		return jobs, nil
	}
	return nil, usageErr("%s: no Argo CD Application or Flux HelmRelease found", file)
}

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

	all, _, err := g.dirJobs(root, root, func(string) bool { return true })
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

// dirJobs collects every target under dir: Argo CD Applications, standalone
// HelmReleases, and HelmReleases produced by building each kustomize overlay.
// Targets that cannot be resolved are returned as failed reports.
func (g *globals) dirJobs(dir, root string, keep func(name string) bool) ([]job, []*report.Check, error) {
	files, err := yamlFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	roots, err := flux.Roots(dir)
	if err != nil {
		return nil, nil, err
	}
	inTree := map[string]bool{}
	for _, r := range roots {
		for _, f := range flux.Tree(r) {
			inTree[normPath(f)] = true
		}
	}

	var jobs []job
	var failed []*report.Check
	var releaseFiles []string
	for _, f := range files {
		app, err := argo.Load(f)
		switch {
		case err == nil:
			if !keep(app.Name) {
				continue
			}
			js, err := g.appJobs(app, root, nil)
			if err != nil {
				var ee *exitError
				if errors.As(err, &ee) && ee.err != nil && strings.HasSuffix(ee.err.Error(), "no Helm source found") {
					continue
				}
				c := report.Failed(app.Name, err)
				c.AppFile = f
				failed = append(failed, c)
				continue
			}
			jobs = append(jobs, js...)
		case !errors.Is(err, argo.ErrNotApplication):
			failed = append(failed, report.Failed(f, err))
		case !inTree[normPath(f)] && g.hasRelease(f):
			releaseFiles = append(releaseFiles, f)
		}
	}

	if len(releaseFiles) > 0 {
		js, fails := g.fluxFileJobs(releaseFiles, root, nil, keep)
		jobs = append(jobs, js...)
		failed = append(failed, fails...)
	}
	for _, r := range roots {
		if !g.treeHasRelease(r) {
			continue
		}
		label := ""
		if len(roots) > 1 || normPath(r) != normPath(dir) {
			if rel, err := filepath.Rel(dir, r); err == nil {
				label = filepath.ToSlash(rel)
			}
		}
		js, fails := g.fluxBuildJobs(r, root, label, keep)
		jobs = append(jobs, js...)
		failed = append(failed, fails...)
	}
	return jobs, failed, nil
}

func (g *globals) hasRelease(file string) bool {
	objs, err := flux.ParseFile(file, g.cfg.Flux.Substitute)
	if err != nil {
		return false
	}
	for _, o := range objs {
		if flux.IsRelease(o) {
			return true
		}
	}
	return false
}

func (g *globals) treeHasRelease(dir string) bool {
	for _, f := range flux.Tree(dir) {
		objs, err := flux.ParseFile(f, g.cfg.Flux.Substitute)
		if err != nil {
			continue
		}
		for _, o := range objs {
			if o.Kind == "HelmRelease" {
				return true
			}
		}
	}
	return false
}

func (g *globals) repoIndex(root string) *flux.Index {
	if g.fluxIndex == nil {
		g.fluxIndex = map[string]*flux.Index{}
	}
	key := normPath(root)
	if ix, ok := g.fluxIndex[key]; ok {
		return ix
	}
	ix, err := flux.IndexRepo(root, g.cfg.Flux.Substitute)
	if err != nil {
		ix = &flux.Index{}
	}
	g.fluxIndex[key] = ix
	return ix
}

// fluxFileJobs reads HelmReleases as they are written in files, resolving
// their sources and valuesFrom against the whole repository.
func (g *globals) fluxFileJobs(files []string, root string, t *targetFlags, keep func(string) bool) ([]job, []*report.Check) {
	res := &flux.Resolver{RepoRoot: root, Sources: g.repoIndex(root)}
	var jobs []job
	var failed []*report.Check
	for _, f := range files {
		objs, err := flux.ParseFile(f, g.cfg.Flux.Substitute)
		if err != nil {
			failed = append(failed, report.Failed(f, err))
			continue
		}
		for _, o := range objs {
			if !flux.IsRelease(o) || (keep != nil && !keep(o.Name)) {
				continue
			}
			rel, err := res.Resolve(o)
			if err != nil {
				c := report.Failed(o.ID(), err)
				c.AppFile = f
				failed = append(failed, c)
				continue
			}
			j, err := releaseJob(rel, "", f, relPath(root, f), t)
			if err != nil {
				failed = append(failed, report.Failed(rel.ID, err))
				continue
			}
			jobs = append(jobs, j)
		}
	}
	return jobs, failed
}

// fluxBuildJobs builds a kustomize overlay and reads the HelmReleases of the
// output, with patches applied. Keys are traced back to the files that set them.
func (g *globals) fluxBuildJobs(dir, root, label string, keep func(string) bool) ([]job, []*report.Check) {
	name := label
	if name == "" {
		name = filepath.Base(dir)
	}
	out, err := flux.Build(dir, g.cfg.Flux.Substitute)
	if err != nil {
		return nil, []*report.Check{report.Failed(name, fmt.Errorf("kustomize build: %w", err))}
	}
	objs, err := flux.ParseYAML(out)
	if err != nil {
		return nil, []*report.Check{report.Failed(name, err)}
	}
	attr := flux.NewAttributor(flux.Tree(dir), g.cfg.Flux.Substitute)
	res := &flux.Resolver{
		RepoRoot:  root,
		Sources:   &flux.Index{Objects: append(append([]*flux.Object{}, flux.TreeGenerators(dir)...), objs...)},
		Fallback:  g.repoIndex(root),
		Attribute: attr.Origins,
	}

	var jobs []job
	var failed []*report.Check
	for _, o := range objs {
		if !flux.IsRelease(o) || (keep != nil && !keep(o.Name)) {
			continue
		}
		rel, err := res.Resolve(o)
		if err != nil {
			failed = append(failed, report.Failed(prefixed(label, o.ID()), err))
			continue
		}
		j, err := releaseJob(rel, label, attr.File(o), relPath(root, dir), nil)
		if err != nil {
			failed = append(failed, report.Failed(prefixed(label, rel.ID), err))
			continue
		}
		jobs = append(jobs, j)
	}
	return jobs, failed
}

func releaseJob(rel *flux.Release, label, file, where string, t *targetFlags) (job, error) {
	j := job{
		name:      prefixed(label, rel.ID),
		app:       rel.Object.Name,
		appFile:   file,
		ident:     "flux:" + where + ":" + rel.ID,
		ref:       rel.Chart,
		release:   rel.ReleaseName,
		namespace: rel.Namespace,
		vals:      rel.Values,
		notes:     rel.Notes,
	}
	if t != nil {
		if err := addExtra(j.vals, t); err != nil {
			return j, err
		}
		if t.release != "" {
			j.release = t.release
		}
		if t.namespace != "" {
			j.namespace = t.namespace
		}
	}
	return j, nil
}

func prefixed(label, id string) string {
	if label == "" {
		return id
	}
	return label + ":" + id
}

func relPath(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(p)
}
