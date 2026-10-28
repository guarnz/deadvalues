package cli

import (
	"fmt"
	"path/filepath"

	"github.com/guarnz/deadvalues/internal/flux"
	"github.com/guarnz/deadvalues/internal/git"
	"github.com/guarnz/deadvalues/internal/report"
)

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
func (g *globals) fluxFileJobs(files []string, root string, t *targetFlags, filter *appFilter) ([]job, []*report.Check) {
	res := &flux.Resolver{RepoRoot: root, Sources: g.repoIndex(root), Origin: git.Origin(root)}
	var jobs []job
	var failed []*report.Check
	for _, f := range files {
		res.Namespace = g.fluxTargetNamespace(root, filepath.Dir(f))
		vars, varNotes := g.fluxVars(root, filepath.Dir(f))
		objs, err := flux.ParseFile(f, vars)
		if err != nil {
			failed = append(failed, failedAt(f, f, err))
			continue
		}
		for _, o := range objs {
			if !flux.IsRelease(o) {
				continue
			}
			rel, err := res.Resolve(o)
			if !filter.keep(f, appNames("", o, rel)...) {
				continue
			}
			if err != nil {
				failed = append(failed, failedAt(o.ID(), f, err))
				continue
			}
			j, err := releaseJob(rel, "", f, relPath(root, f), t)
			if err != nil {
				failed = append(failed, failedAt(rel.ID, f, err))
				continue
			}
			j.notes = append(append([]string{}, j.notes...), varNotes...)
			jobs = append(jobs, j)
		}
	}
	return jobs, failed
}

// fluxBuildJobs builds a kustomize overlay and reads the HelmReleases of the
// output, with patches applied. Keys are traced back to the files that set them.
func (g *globals) fluxBuildJobs(dir, root, label string, filter *appFilter) ([]job, []*report.Check) {
	if filter.skipsDir(dir) {
		return nil, nil
	}
	name := relPath(root, dir)
	vars, varNotes := g.fluxVars(root, dir)
	components := g.fluxComponents(root, dir)
	out, err := flux.Build(dir, vars, components...)
	if err != nil {
		return nil, []*report.Check{failedAt(name, dir, fmt.Errorf("kustomize build: %w", err))}
	}
	objs, err := flux.ParseYAML(out)
	if err != nil {
		return nil, []*report.Check{failedAt(name, dir, err)}
	}
	attr := flux.NewAttributor(flux.Tree(dir, components...), vars)
	res := &flux.Resolver{
		RepoRoot:  root,
		Sources:   &flux.Index{Objects: append(append([]*flux.Object{}, flux.TreeGenerators(dir, components...)...), objs...)},
		Fallback:  g.repoIndex(root),
		Attribute: attr.Origins,
		Namespace: g.fluxTargetNamespace(root, dir),
		Origin:    git.Origin(root),
	}

	var jobs []job
	var failed []*report.Check
	for _, o := range objs {
		if !flux.IsRelease(o) {
			continue
		}
		rel, err := res.Resolve(o)
		if !filter.keep(attr.File(o), appNames(label, o, rel)...) {
			continue
		}
		if err != nil {
			failed = append(failed, failedAt(prefixed(label, o.ID()), attr.File(o), err))
			continue
		}
		j, err := releaseJob(rel, label, attr.File(o), relPath(root, dir), nil)
		if err != nil {
			failed = append(failed, failedAt(prefixed(label, rel.ID), attr.File(o), err))
			continue
		}
		j.notes = append(append([]string{}, j.notes...), varNotes...)
		jobs = append(jobs, j)
	}
	return jobs, failed
}

// fluxTargetNamespace is the targetNamespace of the Flux Kustomization that applies dir.
func (g *globals) fluxTargetNamespace(root, dir string) string {
	if ks := g.repoIndex(root).KustomizationFor(root, dir); ks != nil {
		return flux.Str(ks.Node, "spec", "targetNamespace")
	}
	return ""
}

// fluxVars are the Flux Kustomization's postBuild variables, with flux.substitute on top.
func (g *globals) fluxVars(root, dir string) (map[string]string, []string) {
	vars := map[string]string{}
	var notes []string
	if ks := g.repoIndex(root).KustomizationFor(root, dir); ks != nil {
		vars, notes = g.repoIndex(root).Vars(ks)
	}
	for k, v := range g.cfg.Flux.Substitute {
		vars[k] = v
	}
	return vars, notes
}

// fluxComponents are the spec.components of the Flux Kustomization whose path is dir.
func (g *globals) fluxComponents(root, dir string) []string {
	ks := g.repoIndex(root).KustomizationFor(root, dir)
	if ks == nil || filepath.Clean(filepath.Join(root, flux.Str(ks.Node, "spec", "path"))) != filepath.Clean(dir) {
		return nil
	}
	var out []string
	if list := flux.Get(ks.Node, "spec", "components"); list != nil {
		for _, c := range list.Content {
			out = append(out, c.Value)
		}
	}
	return out
}

// appNames lists the names --only and --skip accept for a HelmRelease: its
// metadata.name, namespace/name and the name the report shows.
func appNames(label string, o *flux.Object, rel *flux.Release) []string {
	names := []string{o.Name, o.ID(), prefixed(label, o.ID())}
	if rel != nil {
		names = append(names, rel.ID, prefixed(label, rel.ID))
	}
	return names
}

func releaseJob(rel *flux.Release, label, file, where string, t *targetFlags) (job, error) {
	j := job{
		name:         prefixed(label, rel.ID),
		app:          rel.Object.Name,
		appFile:      file,
		ident:        "flux:" + where + ":" + rel.ID,
		ref:          rel.Chart,
		release:      rel.ReleaseName,
		namespace:    rel.Namespace,
		vals:         rel.Values,
		notes:        rel.Notes,
		placeholders: rel.Placeholders,
		skip:         rel.Skip,
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
