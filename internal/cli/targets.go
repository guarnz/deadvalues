package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/flux"
	"github.com/guarnz/deadvalues/internal/report"
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
		apps, err := argo.Load(file)
		if err != nil {
			return nil, &exitError{code: 2, err: err}
		}
		if len(apps) == 1 {
			return g.appJobs(apps[0], root, t)
		}
		var jobs []job
		for _, app := range apps {
			js, err := g.appJobs(app, root, t)
			if noHelmSource(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, js...)
		}
		if len(jobs) == 0 {
			return nil, &exitError{code: 2, err: fmt.Errorf("%s: no Application with a Helm source", file)}
		}
		return jobs, nil
	case hasRelease:
		jobs, failed := g.fluxFileJobs([]string{file}, root, t, nil)
		if len(failed) > 0 {
			return nil, &exitError{code: 2, err: errors.New(failed[0].Error)}
		}
		return jobs, nil
	}
	return nil, usageErr("%s: no Argo CD Application or Flux HelmRelease found", file)
}

// dirJobs collects every target under dir (Applications, standalone HelmReleases and
// those built from kustomize overlays); unresolvable ones come back as failed reports.
func (g *globals) dirJobs(dir, root string, filter *appFilter) ([]job, []*report.Check, error) {
	files, err := yamlFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	roots, err := flux.Roots(dir)
	if err != nil {
		return nil, nil, err
	}
	components, err := flux.Components(dir)
	if err != nil {
		return nil, nil, err
	}
	inTree := map[string]bool{}
	for _, r := range slices.Concat(roots, components) {
		for _, f := range flux.Tree(r) {
			inTree[normPath(f)] = true
		}
	}

	jobs, failed, cover := g.graphJobs(dir, root, filter)
	notApplied := g.fluxGraph(root).Complete()
	var releaseFiles []string
	covered := map[string]bool{}
	for _, f := range files {
		apps, err := argo.Load(f)
		switch {
		case err == nil:
			for _, app := range apps {
				for _, s := range app.Sources {
					if s.Path != "" {
						covered[normPath(filepath.Join(root, filepath.FromSlash(s.Path)))] = true
					}
				}
				if !filter.keep(f, app.Name) {
					continue
				}
				js, err := g.appJobs(app, root, nil)
				if noHelmSource(err) {
					continue
				}
				if err != nil {
					failed = append(failed, failedAt(app.Name, f, err))
					continue
				}
				jobs = append(jobs, js...)
			}
		case !errors.Is(err, argo.ErrNotApplication):
			failed = append(failed, failedAt(f, f, err))
		case !inTree[normPath(f)] && !cover.files[normPath(f)] && g.hasRelease(f):
			releaseFiles = append(releaseFiles, f)
		}
	}

	for _, r := range roots {
		if covered[normPath(r)] {
			continue
		}
		js, fails := g.kustomizeJobs(r, root, filter)
		jobs = append(jobs, js...)
		failed = append(failed, fails...)
	}
	switch {
	case len(releaseFiles) > 0 && notApplied:
		jobs = append(jobs, g.notAppliedJobs(releaseFiles, root, "", "", filter)...)
	case len(releaseFiles) > 0:
		js, fails := g.fluxFileJobs(releaseFiles, root, nil, filter)
		jobs = append(jobs, js...)
		failed = append(failed, fails...)
	}
	for _, r := range roots {
		if !g.treeHasRelease(r) || cover.overlay(r) {
			continue
		}
		label := ""
		if len(roots) > 1 || normPath(r) != normPath(dir) {
			if rel, err := filepath.Rel(dir, r); err == nil {
				label = filepath.ToSlash(rel)
			}
		}
		if notApplied {
			jobs = append(jobs, g.notAppliedJobs(flux.Tree(r), root, label, r, filter)...)
			continue
		}
		js, fails := g.fluxBuildJobs(r, root, label, filter)
		jobs = append(jobs, js...)
		failed = append(failed, fails...)
	}
	return append(jobs, g.unsupportedJobs(dir, root, files, filter)...), failed, nil
}

// failedAt reports a target that cannot be resolved, with the file or overlay
// directory it comes from, so --changed-since can tell whether a change touches it.
func failedAt(name, file string, err error) *report.Check {
	c := report.Failed(name, err)
	c.AppFile = file
	return c
}

// noHelmSource reports whether appJobs failed only because the Application has no Helm source.
func noHelmSource(err error) bool {
	var ee *exitError
	return errors.As(err, &ee) && ee.err != nil && strings.HasSuffix(ee.err.Error(), "no Helm source found")
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
