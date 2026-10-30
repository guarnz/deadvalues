package flux

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"

	"github.com/guarnz/deadvalues/internal/values"
)

var kustomizationNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

type kustomization struct {
	Kind       string   `json:"kind"`
	Namespace  string   `json:"namespace"`
	Resources  []string `json:"resources"`
	Components []string `json:"components"`
	Patches    []struct {
		Path string `json:"path"`
	} `json:"patches"`
	PatchesStrategicMerge []string `json:"patchesStrategicMerge"`
	ConfigMapGenerator    []struct {
		Name      string   `json:"name"`
		Namespace string   `json:"namespace"`
		Files     []string `json:"files"`
	} `json:"configMapGenerator"`
}

func isKustomizationFile(p string) bool {
	base := filepath.Base(p)
	for _, n := range kustomizationNames {
		if base == n {
			return true
		}
	}
	return false
}

func readKustomization(dir string) (*kustomization, bool) {
	for _, n := range kustomizationNames {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var k kustomization
		if err := yaml.Unmarshal(data, &k); err != nil {
			return nil, false
		}
		return &k, true
	}
	return nil, false
}

// IsKustomizeDir reports whether dir holds a kustomization file.
func IsKustomizeDir(dir string) bool {
	_, ok := readKustomization(dir)
	return ok
}

func isRemote(r string) bool {
	return strings.Contains(r, "://") || strings.HasPrefix(r, "github.com/") || strings.Contains(r, "?ref=")
}

// Roots returns the kustomize directories under dir that no other kustomization
// includes: the overlays to build. Components are left out; dir itself is always a root.
func Roots(dir string) ([]string, error) {
	roots, _, err := walkKustomizations(dir)
	return roots, err
}

// Components returns the kustomize Components under dir, built only by what includes them.
func Components(dir string) ([]string, error) {
	_, components, err := walkKustomizations(dir)
	return components, err
}

func walkKustomizations(dir string) (roots, components []string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	if IsKustomizeDir(abs) {
		return []string{abs}, nil, nil
	}
	var found []string
	referenced := map[string]bool{}
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != abs && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		k, ok := readKustomization(p)
		if !ok {
			return nil
		}
		for _, r := range append(append([]string{}, k.Resources...), k.Components...) {
			if isRemote(r) {
				continue
			}
			referenced[filepath.Clean(filepath.Join(p, r))] = true
		}
		if k.Kind == "Component" {
			components = append(components, p)
			return nil
		}
		found = append(found, p)
		return nil
	})
	for _, p := range found {
		if !referenced[filepath.Clean(p)] {
			roots = append(roots, p)
		}
	}
	return roots, components, err
}

// generatedResources lists what Flux puts in the kustomization it generates for a path
// without one: subdirectories with a kustomization, and the YAML files of the rest.
func generatedResources(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || fileExists(filepath.Join(p, "Chart.yaml")) {
				return filepath.SkipDir
			}
			if IsKustomizeDir(p) {
				out = append(out, filepath.ToSlash(rel))
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(p); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		if objs, err := ParseFile(p, nil); err == nil && len(objs) > 0 {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// pathKustomization reads the kustomization of a Flux path, or the one Flux generates.
func pathKustomization(dir string) *kustomization {
	if k, ok := readKustomization(dir); ok {
		return k
	}
	return &kustomization{Resources: generatedResources(dir)}
}

// Tree returns the local files that make up the build of dir, in kustomize
// order: resources (recursively) first, then patches.
func Tree(dir string, components ...string) []string {
	var out []string
	seen := map[string]bool{}
	top := filepath.Clean(dir)
	var walk func(dir string, extra []string)
	walk = func(dir string, extra []string) {
		dir = filepath.Clean(dir)
		if seen[dir] {
			return
		}
		seen[dir] = true
		k, ok := readKustomization(dir)
		if !ok && dir == top {
			k, ok = pathKustomization(dir), true
		}
		if !ok {
			return
		}
		for _, r := range append(append(append([]string{}, k.Resources...), k.Components...), extra...) {
			if isRemote(r) {
				continue
			}
			p := filepath.Join(dir, r)
			info, err := os.Stat(p)
			switch {
			case err != nil:
			case info.IsDir():
				walk(p, nil)
			default:
				out = append(out, p)
			}
		}
		for _, p := range k.Patches {
			if p.Path != "" {
				out = append(out, filepath.Join(dir, p.Path))
			}
		}
		for _, p := range k.PatchesStrategicMerge {
			if !strings.Contains(p, "\n") {
				out = append(out, filepath.Join(dir, p))
			}
		}
	}
	walk(dir, components)
	return out
}

// TreeGenerators returns the ConfigMaps declared by configMapGenerator
// entries anywhere in dir's build.
func TreeGenerators(dir string, components ...string) []*Object {
	var out []*Object
	seen := map[string]bool{}
	top := filepath.Clean(dir)
	var walk func(dir string, extra []string)
	walk = func(dir string, extra []string) {
		dir = filepath.Clean(dir)
		if seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, generated(dir)...)
		k, ok := readKustomization(dir)
		if !ok && dir == top {
			k, ok = pathKustomization(dir), true
		}
		if !ok {
			return
		}
		for _, r := range append(append(append([]string{}, k.Resources...), k.Components...), extra...) {
			if p := filepath.Join(dir, r); !isRemote(r) {
				if info, err := os.Stat(p); err == nil && info.IsDir() {
					walk(p, nil)
				}
			}
		}
	}
	walk(dir, components)
	return out
}

func generated(dir string) []*Object {
	k, ok := readKustomization(dir)
	if !ok {
		return nil
	}
	var out []*Object
	for _, g := range k.ConfigMapGenerator {
		ns := g.Namespace
		if ns == "" {
			ns = k.Namespace
		}
		files := map[string]string{}
		for _, f := range g.Files {
			key, path, ok := strings.Cut(f, "=")
			if !ok {
				key, path = filepath.Base(f), f
			}
			abs, err := filepath.Abs(filepath.Join(dir, path))
			if err != nil {
				continue
			}
			files[key] = abs
		}
		out = append(out, &Object{Kind: "ConfigMap", Name: g.Name, Namespace: ns, File: filepath.Join(dir, "kustomization.yaml"), Gen: files})
	}
	return out
}

// Spec is what a Flux Kustomization adds to the kustomization of its path.
type Spec struct {
	Namespace  string
	Components []string
	Patches    []interface{}
}

// Build runs kustomize on dir in process, the way Flux's kustomize-controller
// does, adding Flux components and applying ${var} substitution to the output.
func Build(dir string, vars map[string]string, components ...string) ([]byte, error) {
	return BuildSpec(dir, vars, Spec{Components: components})
}

// BuildSpec builds dir with a Flux Kustomization's spec: targetNamespace, components
// and patches go into the kustomization, generated when dir has none, as in Flux.
func BuildSpec(dir string, vars map[string]string, spec Spec) ([]byte, error) {
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	opts.PluginConfig = types.DisabledPluginConfig()
	fsys := filesys.MakeFsOnDisk()
	_, has := readKustomization(dir)
	if !has || spec.Namespace != "" || len(spec.Components) > 0 || len(spec.Patches) > 0 {
		fsys = withSpec{FileSystem: fsys, dir: canonical(dir), spec: spec, generate: !has}
	}
	m, err := krusty.MakeKustomizer(opts).Run(fsys, dir)
	if err != nil {
		return nil, err
	}
	out, err := m.AsYaml()
	if err != nil {
		return nil, err
	}
	return substituteBuild(out, vars), nil
}

// withSpec reads dir's kustomization with a Flux Kustomization's spec on top, without
// changing the file; generate makes one up for a dir that has none.
type withSpec struct {
	filesys.FileSystem
	dir      string
	spec     Spec
	generate bool
}

func (f withSpec) ReadFile(path string) ([]byte, error) {
	if !slices.Contains(kustomizationNames, filepath.Base(path)) || canonical(filepath.Dir(path)) != f.dir {
		return f.FileSystem.ReadFile(path)
	}
	var k map[string]interface{}
	if f.generate {
		if filepath.Base(path) != kustomizationNames[0] {
			return nil, os.ErrNotExist
		}
		k = map[string]interface{}{"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization"}
		var res []interface{}
		for _, r := range generatedResources(filepath.Dir(path)) {
			res = append(res, r)
		}
		k["resources"] = res
	} else {
		data, err := f.FileSystem.ReadFile(path)
		if err != nil {
			return data, err
		}
		if err := yaml.Unmarshal(data, &k); err != nil || k == nil {
			return data, nil
		}
	}
	if f.spec.Namespace != "" {
		k["namespace"] = f.spec.Namespace
	}
	if len(f.spec.Components) > 0 {
		list, _ := k["components"].([]interface{})
		for _, c := range f.spec.Components {
			list = append(list, c)
		}
		k["components"] = list
	}
	if len(f.spec.Patches) > 0 {
		list, _ := k["patches"].([]interface{})
		k["patches"] = append(list, f.spec.Patches...)
	}
	return yaml.Marshal(k)
}

// canonical spells a path one way: absolute, symlinks resolved, lower case on Windows.
func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// Attributor traces keys of a built HelmRelease back to the files of the
// build that set them: the base release and every patch, in kustomize order.
type Attributor struct {
	docs []*Object
}

func NewAttributor(files []string, vars map[string]string) *Attributor {
	a := &Attributor{}
	for _, f := range files {
		objs, err := ParseFile(f, vars)
		if err != nil {
			continue
		}
		for _, o := range objs {
			if o.Kind == "HelmRelease" {
				a.docs = append(a.docs, o)
			}
		}
	}
	return a
}

// docsFor returns the documents of the release: same name and namespace, or, when a
// targetNamespace or kustomize namespace replaced it, the same name.
func (a *Attributor) docsFor(rel *Object) []*Object {
	var same, named []*Object
	for _, d := range a.docs {
		if d.Name != rel.Name {
			continue
		}
		named = append(named, d)
		if d.Namespace == "" || rel.Namespace == "" || d.Namespace == rel.Namespace {
			same = append(same, d)
		}
	}
	if len(same) > 0 {
		return same
	}
	return named
}

// Origins lists every file of the build whose HelmRelease sets spec.values.<path>.
func (a *Attributor) Origins(rel *Object, path []string) []values.Origin {
	full := append([]string{"spec", "values"}, path...)
	var out []values.Origin
	for _, d := range a.docsFor(rel) {
		if k, _ := lookup(d.Node, full); k != nil {
			out = append(out, values.Origin{File: d.File, Line: k.Line, IsFile: true, Doc: d.Doc, Prefix: []string{"spec", "values"}})
		}
	}
	return out
}

// File returns the file holding the base definition of the release.
func (a *Attributor) File(rel *Object) string {
	docs := a.docsFor(rel)
	for _, d := range docs {
		if IsRelease(d) {
			return d.File
		}
	}
	if len(docs) > 0 {
		return docs[0].File
	}
	return ""
}
