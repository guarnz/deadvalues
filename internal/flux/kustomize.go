package flux

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"

	"github.com/guarnz/deadvalues/internal/values"
)

var kustomizationNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

type kustomization struct {
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

// Roots returns the kustomize directories under dir that no other
// kustomization under dir includes: the overlays to build. When dir itself
// is a kustomize directory it is the only root.
func Roots(dir string) ([]string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if IsKustomizeDir(abs) {
		return []string{abs}, nil
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
		found = append(found, p)
		for _, r := range append(append([]string{}, k.Resources...), k.Components...) {
			if isRemote(r) {
				continue
			}
			referenced[filepath.Clean(filepath.Join(p, r))] = true
		}
		return nil
	})
	var roots []string
	for _, p := range found {
		if !referenced[filepath.Clean(p)] {
			roots = append(roots, p)
		}
	}
	return roots, err
}

// Tree returns the local files that make up the build of dir, in kustomize
// order: resources (recursively) first, then patches.
func Tree(dir string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		dir = filepath.Clean(dir)
		if seen[dir] {
			return
		}
		seen[dir] = true
		k, ok := readKustomization(dir)
		if !ok {
			return
		}
		for _, r := range append(append([]string{}, k.Resources...), k.Components...) {
			if isRemote(r) {
				continue
			}
			p := filepath.Join(dir, r)
			info, err := os.Stat(p)
			switch {
			case err != nil:
			case info.IsDir():
				walk(p)
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
	walk(dir)
	return out
}

// TreeGenerators returns the ConfigMaps declared by configMapGenerator
// entries anywhere in dir's build.
func TreeGenerators(dir string) []*Object {
	var out []*Object
	seen := map[string]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		dir = filepath.Clean(dir)
		if seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, generated(dir)...)
		k, ok := readKustomization(dir)
		if !ok {
			return
		}
		for _, r := range append(append([]string{}, k.Resources...), k.Components...) {
			if p := filepath.Join(dir, r); !isRemote(r) {
				if info, err := os.Stat(p); err == nil && info.IsDir() {
					walk(p)
				}
			}
		}
	}
	walk(dir)
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

// Build runs kustomize on dir in process, the way Flux's kustomize-controller
// does, and applies ${var} substitution to the output.
func Build(dir string, vars map[string]string) ([]byte, error) {
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	opts.PluginConfig = types.DisabledPluginConfig()
	m, err := krusty.MakeKustomizer(opts).Run(filesys.MakeFsOnDisk(), dir)
	if err != nil {
		return nil, err
	}
	out, err := m.AsYaml()
	if err != nil {
		return nil, err
	}
	return Substitute(out, vars), nil
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

func (a *Attributor) matches(d, rel *Object) bool {
	return d.Name == rel.Name && (d.Namespace == "" || rel.Namespace == "" || d.Namespace == rel.Namespace)
}

// Origins lists every file of the build whose HelmRelease sets spec.values.<path>.
func (a *Attributor) Origins(rel *Object, path []string) []values.Origin {
	full := append([]string{"spec", "values"}, path...)
	var out []values.Origin
	for _, d := range a.docs {
		if !a.matches(d, rel) {
			continue
		}
		if k, _ := lookup(d.Node, full); k != nil {
			out = append(out, values.Origin{File: d.File, Line: k.Line, IsFile: true, Doc: d.Doc, Prefix: []string{"spec", "values"}})
		}
	}
	return out
}

// File returns the file holding the base definition of the release.
func (a *Attributor) File(rel *Object) string {
	for _, d := range a.docs {
		if a.matches(d, rel) && IsRelease(d) {
			return d.File
		}
	}
	for _, d := range a.docs {
		if a.matches(d, rel) {
			return d.File
		}
	}
	return ""
}
