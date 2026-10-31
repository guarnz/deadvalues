package flux

import (
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/guarnz/deadvalues/internal/git"
)

// Applied is a Flux Kustomization the graph reached, as its parent applies it,
// with the objects its build outputs.
type Applied struct {
	Kustomization *Object
	Cluster       string
	Dir           string
	Spec          Spec
	Vars          map[string]string
	VarNotes      []string
	Objects       []*Object
	Err           error
}

// Graph is what the Flux Kustomizations of a repository apply, followed from each
// cluster's root the way the kustomize-controller does.
type Graph struct {
	Applied []*Applied
	// Explicit is set when the roots are declared, so what they do not reach is not applied.
	Explicit bool
	// templated is set when a flux-operator ResourceSet generates Kustomizations the graph cannot follow.
	templated bool
}

// Complete reports whether the roots are declared and every build of the graph worked:
// only then is what the graph does not reach known to be not applied.
func (g *Graph) Complete() bool {
	if !g.Explicit || g.templated {
		return false
	}
	for _, a := range g.Applied {
		if a.Err != nil {
			return false
		}
	}
	return true
}

type graphRoot struct {
	dir string
	ks  *Object
	// home is the directory that names the cluster, when it is not dir.
	home string
}

func (r graphRoot) cluster() string {
	if r.home != "" {
		return r.home
	}
	return r.dir
}

// NewGraph follows the Flux Kustomizations of the repository from each cluster root:
// a FluxInstance sync path, or a Kustomization that applies itself; else, those no build applies.
func NewGraph(repoRoot string, ix *Index, origin string, extra map[string]string) *Graph {
	g := &Graph{}
	for _, o := range ix.Objects {
		if o.Kind == "ResourceSet" && generatesKustomization(o) {
			g.templated = true
		}
	}
	orphans := orphanRoots(repoRoot, ix, origin)
	roots := explicitRoots(repoRoot, ix, origin, orphans)
	g.Explicit = len(roots) > 0
	labels := clusterLabels(repoRoot, roots)
	if !g.Explicit {
		roots = orphans
		labels = make([]string, len(roots))
	}
	seen := map[string]bool{}
	var visit func(ks *Object, dir, cluster string, scope []*Object)
	visit = func(ks *Object, dir, cluster string, scope []*Object) {
		key := cluster + "|" + canonical(dir)
		if ks != nil {
			key += "|" + ks.ID()
		}
		if seen[key] {
			return
		}
		seen[key] = true
		a := apply(ks, dir, cluster, ix, &Index{Objects: scope}, extra)
		g.Applied = append(g.Applied, a)
		inner := append(append([]*Object{}, a.Objects...), scope...)
		for _, o := range a.Objects {
			if isFluxKustomization(o) && followed(o, ix, origin) {
				visit(o, pathOf(repoRoot, o), cluster, inner)
			}
		}
	}
	for i, r := range roots {
		visit(r.ks, r.dir, labels[i], nil)
	}
	return g
}

// apply builds a Kustomization; scope holds what the builds above it output, where
// its substituteFrom ConfigMaps and Secrets are looked up first.
func apply(ks *Object, dir, cluster string, ix, scope *Index, extra map[string]string) *Applied {
	a := &Applied{Kustomization: ks, Cluster: cluster, Dir: dir, Vars: map[string]string{}}
	if ks != nil {
		a.Vars, a.VarNotes = ix.VarsIn(ks, scope)
		a.Spec = SpecOf(ks)
	}
	for k, v := range extra {
		a.Vars[k] = v
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		a.Err = os.ErrNotExist
		return a
	}
	out, err := BuildSpec(dir, a.Vars, a.Spec)
	if err != nil {
		a.Err = err
		return a
	}
	a.Objects, a.Err = ParseYAML(out)
	return a
}

// SpecOf reads the targetNamespace, components and patches of a Flux Kustomization.
func SpecOf(ks *Object) Spec {
	s := Spec{Namespace: Str(ks.Node, "spec", "targetNamespace")}
	if list := Get(ks.Node, "spec", "components"); list != nil {
		for _, c := range list.Content {
			s.Components = append(s.Components, c.Value)
		}
	}
	if p := Get(ks.Node, "spec", "patches"); p != nil {
		var patches []interface{}
		if err := p.Decode(&patches); err == nil {
			s.Patches = patches
		}
	}
	return s
}

// generatesKustomization reports whether a ResourceSet's resources or template make a Flux Kustomization.
func generatesKustomization(rs *Object) bool {
	if list := Get(rs.Node, "spec", "resources"); list != nil {
		for _, r := range list.Content {
			if Str(r, "kind") == "Kustomization" {
				return true
			}
		}
	}
	return strings.Contains(Str(rs.Node, "spec", "resourcesTemplate"), "kind: Kustomization")
}

func isFluxKustomization(o *Object) bool {
	return o.Kind == "Kustomization" && strings.HasPrefix(o.APIVersion, "kustomize.toolkit.fluxcd.io/")
}

func pathOf(repoRoot string, ks *Object) string {
	return filepath.Join(repoRoot, filepath.FromSlash(strings.TrimPrefix(Str(ks.Node, "spec", "path"), "/")))
}

// followed reports whether a Kustomization's source is this repository: no sourceRef,
// or a GitRepository that is not in the repository or points to the origin.
func followed(ks *Object, ix *Index, origin string) bool {
	kind, name := Str(ks.Node, "spec", "sourceRef", "kind"), Str(ks.Node, "spec", "sourceRef", "name")
	switch kind {
	case "":
		return true
	case "GitRepository":
		ns := Str(ks.Node, "spec", "sourceRef", "namespace")
		if ns == "" {
			ns = ks.Namespace
		}
		src := ix.Find(kind, name, ns, ks.File)
		if src == nil {
			return true
		}
		url := Str(src.Node, "spec", "url")
		return url == "" || origin == "" || git.SameRepository(url, origin) || git.SamePath(url, origin)
	default:
		return false
	}
}

// explicitRoots are the declared cluster roots: FluxInstance syncs (as objects or as
// flux-instance chart values), bootstrap Kustomizations, and the orphans of a directory
// beside a root, such as clusters/staging next to clusters/production.
func explicitRoots(repoRoot string, ix *Index, origin string, orphans []graphRoot) []graphRoot {
	var out []graphRoot
	seen := map[string]bool{}
	add := func(r graphRoot) {
		key := canonical(r.dir)
		if r.ks != nil {
			key += "|" + r.ks.ID()
		}
		if info, err := os.Stat(r.dir); err != nil || !info.IsDir() || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, r)
	}
	sync := func(s *yaml.Node) {
		kind, url, p := Str(s, "kind"), Str(s, "url"), Str(s, "path")
		if p == "" || kind != "" && kind != "GitRepository" || url != "" && origin != "" && !git.SameRepository(url, origin) && !git.SamePath(url, origin) {
			return
		}
		add(graphRoot{dir: filepath.Join(repoRoot, filepath.FromSlash(strings.TrimPrefix(p, "/")))})
	}
	for _, o := range ix.Objects {
		switch {
		case o.Kind == "FluxInstance":
			sync(Get(o.Node, "spec", "sync"))
		case o.Kind == "HelmRelease":
			if s := instanceSync(o, ix); s != nil {
				sync(s)
			}
		}
	}
	for _, r := range orphans {
		if isBootstrap(r) {
			add(r)
		}
	}
	parents, homes := map[string]bool{}, map[string]bool{}
	for _, r := range out {
		parents[canonical(filepath.Dir(r.cluster()))] = true
		homes[canonical(r.cluster())] = true
	}
	for _, r := range orphans {
		home := filepath.Dir(r.ks.File)
		if parents[canonical(filepath.Dir(home))] && !homes[canonical(home)] {
			r.home = home
			add(r)
		}
	}
	return out
}

// isBootstrap reports whether an orphan Kustomization is what `flux bootstrap` writes:
// a gotk-sync file, or a flux-system that applies itself. An app's own ks.yaml can too.
func isBootstrap(r graphRoot) bool {
	if b := filepath.Base(r.ks.File); b == "gotk-sync.yaml" || b == "gotk-sync.yml" {
		return true
	}
	if r.ks.Name != "flux-system" {
		return false
	}
	for _, f := range Tree(r.dir, SpecOf(r.ks).Components...) {
		if canonical(f) == canonical(r.ks.File) {
			return true
		}
	}
	return false
}

// instanceSync returns the instance.sync of a flux-instance chart's values: in spec.values,
// or in a values file that a valuesFrom ConfigMap is generated from.
func instanceSync(rel *Object, ix *Index) *yaml.Node {
	if s := Get(rel.Node, "spec", "values", "instance", "sync"); s != nil {
		return s
	}
	from := Get(rel.Node, "spec", "valuesFrom")
	if from == nil {
		return nil
	}
	for _, vf := range from.Content {
		if Str(vf, "kind") != "ConfigMap" {
			continue
		}
		cm := ix.Find("ConfigMap", Str(vf, "name"), rel.Namespace, rel.File)
		if cm == nil || cm.Gen == nil {
			continue
		}
		key := Str(vf, "valuesKey")
		if key == "" {
			key = "values.yaml"
		}
		file, ok := cm.Gen[key]
		if !ok {
			continue
		}
		if vals, err := parseValues(file); err == nil {
			if s := Get(vals, "instance", "sync"); s != nil {
				return s
			}
		}
	}
	return nil
}

func parseValues(file string) (*yaml.Node, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var n yaml.Node
	if err := yaml.Unmarshal(data, &n); err != nil || len(n.Content) == 0 {
		return nil, err
	}
	return n.Content[0], nil
}

// orphanRoots are the Kustomizations that no other Kustomization's build includes,
// for repositories whose cluster root lives outside them.
func orphanRoots(repoRoot string, ix *Index, origin string) []graphRoot {
	var kss []*Object
	for _, o := range ix.Objects {
		if isFluxKustomization(o) && o.File != "" {
			kss = append(kss, o)
		}
	}
	included := map[string]bool{}
	for _, k := range kss {
		for _, f := range Tree(pathOf(repoRoot, k), SpecOf(k).Components...) {
			if c := canonical(f); c != canonical(k.File) {
				included[c] = true
			}
		}
	}
	var out []graphRoot
	for _, k := range kss {
		if !included[canonical(k.File)] && followed(k, ix, origin) {
			out = append(out, graphRoot{dir: pathOf(repoRoot, k), ks: k})
		}
	}
	return out
}

// clusterLabels names each root by the part of its path the others do not share:
// kubernetes/main/flux and kubernetes/utility/flux become main and utility.
// A root inside another root's path is the same cluster declared twice and shares its label.
func clusterLabels(repoRoot string, roots []graphRoot) []string {
	var homes []graphRoot
	index := map[string]int{}
	for _, r := range roots {
		c := canonical(r.cluster())
		if _, ok := index[c]; !ok {
			index[c] = len(homes)
			homes = append(homes, graphRoot{dir: r.cluster()})
		}
	}
	names := homeLabels(repoRoot, homes)
	labels := make([]string, len(roots))
	for i, r := range roots {
		labels[i] = names[index[canonical(r.cluster())]]
	}
	return labels
}

func homeLabels(repoRoot string, roots []graphRoot) []string {
	labels := make([]string, len(roots))
	outer := make([]int, len(roots))
	var tops []graphRoot
	topIndex := map[int]int{}
	for i, r := range roots {
		outer[i] = -1
		for j, o := range roots {
			if i != j && canonical(o.dir) != canonical(r.dir) && strings.HasPrefix(canonical(r.dir), canonical(o.dir)+string(filepath.Separator)) {
				outer[i] = j
				break
			}
		}
		if outer[i] < 0 {
			topIndex[i] = len(tops)
			tops = append(tops, r)
		}
	}
	top := topLabels(repoRoot, tops)
	for i := range roots {
		k := i
		for outer[k] >= 0 {
			k = outer[k]
		}
		labels[i] = top[topIndex[k]]
	}
	return labels
}

func topLabels(repoRoot string, roots []graphRoot) []string {
	labels := make([]string, len(roots))
	if len(roots) < 2 {
		return labels
	}
	parts := make([][]string, len(roots))
	for i, r := range roots {
		rel, err := filepath.Rel(repoRoot, r.dir)
		if err != nil {
			rel = r.dir
		}
		parts[i] = strings.Split(filepath.ToSlash(rel), "/")
	}
	prefix, suffix := len(parts[0]), len(parts[0])
	for _, p := range parts[1:] {
		n := 0
		for n < prefix && n < len(p) && p[n] == parts[0][n] {
			n++
		}
		prefix = n
		m := 0
		for m < suffix && m < len(p) && p[len(p)-1-m] == parts[0][len(parts[0])-1-m] {
			m++
		}
		suffix = m
	}
	for i, p := range parts {
		end := len(p) - suffix
		if end <= prefix {
			labels[i] = strings.Join(p, "/")
			continue
		}
		labels[i] = strings.Join(p[prefix:end], "/")
	}
	return labels
}
