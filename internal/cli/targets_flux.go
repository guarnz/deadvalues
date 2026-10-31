package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/guarnz/deadvalues/internal/flux"
	"github.com/guarnz/deadvalues/internal/git"
	"github.com/guarnz/deadvalues/internal/report"
	"github.com/guarnz/deadvalues/internal/values"
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
	spec := flux.Spec{Components: g.fluxComponents(root, dir)}
	out, err := flux.Build(dir, vars, spec.Components...)
	if err != nil {
		return nil, []*report.Check{failedAt(name, dir, fmt.Errorf("kustomize build: %w", err))}
	}
	objs, err := flux.ParseYAML(out)
	if err != nil {
		return nil, []*report.Check{failedAt(name, dir, err)}
	}
	b := build{dir: dir, spec: spec, vars: vars, varNotes: varNotes, namespace: g.fluxTargetNamespace(root, dir), label: label, where: relPath(root, dir)}
	return g.buildJobs(objs, root, b, filter, nil)
}

// build is a kustomize build of a Flux path and what its HelmReleases are read with.
type build struct {
	dir                     string
	spec                    flux.Spec
	vars                    map[string]string
	varNotes                []string
	namespace, label, where string
	cluster                 string
}

// buildJobs reads the HelmReleases of a build output; keep, when set, picks them by file.
func (g *globals) buildJobs(objs []*flux.Object, root string, b build, filter *appFilter, keep func(file string) bool) ([]job, []*report.Check) {
	attr := flux.NewAttributor(flux.Tree(b.dir, b.spec.Components...), b.vars)
	res := &flux.Resolver{
		RepoRoot:  root,
		Sources:   &flux.Index{Objects: append(append([]*flux.Object{}, flux.TreeGenerators(b.dir, b.spec.Components...)...), objs...)},
		Fallback:  g.repoIndex(root),
		Attribute: attr.Origins,
		Namespace: b.namespace,
		Origin:    git.Origin(root),
	}

	var jobs []job
	var failed []*report.Check
	for _, o := range objs {
		if !flux.IsRelease(o) {
			continue
		}
		file := attr.File(o)
		if keep != nil && !keep(file) {
			continue
		}
		rel, err := res.Resolve(o)
		if !filter.keep(file, appNames(b.label, o, rel)...) {
			continue
		}
		if err != nil {
			failed = append(failed, failedAt(prefixed(b.label, o.ID()), file, err))
			continue
		}
		j, err := releaseJob(rel, b.label, file, b.where, nil)
		if err != nil {
			failed = append(failed, failedAt(prefixed(b.label, rel.ID), file, err))
			continue
		}
		j.notes = append(append([]string{}, j.notes...), b.varNotes...)
		j.cluster = b.cluster
		jobs = append(jobs, j)
	}
	return jobs, failed
}

func (g *globals) fluxGraph(root string) *flux.Graph {
	if g.fluxGraphs == nil {
		g.fluxGraphs = map[string]*flux.Graph{}
	}
	key := normPath(root)
	if gr, ok := g.fluxGraphs[key]; ok {
		return gr
	}
	gr := flux.NewGraph(root, g.repoIndex(root), git.Origin(root), g.cfg.Flux.Substitute)
	g.fluxGraphs[key] = gr
	return gr
}

// graphCover is what the builds of the Flux graph read: their files and directories.
type graphCover struct {
	files map[string]bool
	dirs  []string
}

// overlay reports whether the graph already read a kustomize overlay: every file of it,
// and a build of it, inside it or around it, as a base shared with another overlay proves nothing.
func (c graphCover) overlay(dir string) bool {
	files := flux.Tree(dir)
	for _, f := range files {
		if !c.files[normPath(f)] {
			return false
		}
	}
	d := normPath(dir)
	sep := string(filepath.Separator)
	return len(files) > 0 && slices.ContainsFunc(c.dirs, func(b string) bool {
		return b == d || strings.HasPrefix(b, d+sep) || strings.HasPrefix(d, b+sep)
	})
}

// graphJobs reads the HelmReleases each Kustomization of the Flux graph applies,
// keeping those whose file is under dir.
func (g *globals) graphJobs(dir, root string, filter *appFilter) ([]job, []*report.Check, graphCover) {
	cover := graphCover{files: map[string]bool{}}
	var jobs []job
	var failed []*report.Check
	seen := map[string]bool{}
	for _, a := range g.fluxGraph(root).Applied {
		if a.Err != nil {
			continue
		}
		cover.dirs = append(cover.dirs, normPath(a.Dir))
		for _, f := range flux.Tree(a.Dir, a.Spec.Components...) {
			cover.files[normPath(f)] = true
		}
		if filter.skipsDir(a.Dir) {
			continue
		}
		label := relPath(root, a.Dir)
		if normPath(a.Dir) == normPath(dir) {
			label = ""
		} else if r, err := filepath.Rel(dir, a.Dir); err == nil && !strings.HasPrefix(r, "..") {
			label = filepath.ToSlash(r)
		}
		label = strings.TrimPrefix(label, a.Cluster+"/")
		b := build{dir: a.Dir, spec: a.Spec, vars: a.Vars, varNotes: a.VarNotes, namespace: a.Spec.Namespace,
			label: prefixed(a.Cluster, label), where: prefixed(a.Cluster, relPath(root, a.Dir)), cluster: "flux|" + a.Cluster}
		js, fails := g.buildJobs(a.Objects, root, b, filter, func(file string) bool {
			return file != "" && underDir(dir, file)
		})
		for _, j := range js {
			if key := a.Cluster + "|" + normPath(j.appFile) + "|" + j.namespace + "/" + j.app; !seen[key] {
				seen[key] = true
				jobs = append(jobs, j)
			}
		}
		failed = append(failed, fails...)
	}
	return jobs, failed, cover
}

// notAppliedJobs lists the HelmReleases of files no Kustomization of the graph applies;
// overlay, when set, is the kustomize directory they are built from.
func (g *globals) notAppliedJobs(files []string, root, label, overlay string, filter *appFilter) []job {
	var jobs []job
	for _, f := range files {
		objs, err := flux.ParseFile(f, g.cfg.Flux.Substitute)
		if err != nil {
			continue
		}
		what := relPath(root, f)
		if overlay != "" {
			what = relPath(root, overlay)
		}
		for _, o := range objs {
			if !flux.IsRelease(o) || !filter.keep(f, appNames(label, o, nil)...) {
				continue
			}
			jobs = append(jobs, job{name: prefixed(label, o.ID()), app: o.Name, appFile: f, ident: "flux:" + what + ":" + o.ID(), vals: values.New(),
				skip: "not applied: no Flux Kustomization of the repository applies " + what})
		}
	}
	return jobs
}

func underDir(dir, file string) bool {
	r, err := filepath.Rel(dir, file)
	return err == nil && !strings.HasPrefix(r, "..") && !filepath.IsAbs(r)
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
