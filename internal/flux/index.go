// Package flux reads Flux HelmReleases and turns them into chart + values
// targets, resolving their sources, valuesFrom and kustomize overlays.
package flux

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Object is one Kubernetes object found in the repository or in a kustomize
// build output.
type Object struct {
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	// File is empty for objects that only exist in a build output.
	File string
	Doc  int
	Node *yaml.Node
	// Gen is set for ConfigMaps produced by a kustomize configMapGenerator:
	// data key -> source file.
	Gen map[string]string
}

func (o *Object) ID() string {
	if o.Namespace == "" {
		return o.Name
	}
	return o.Namespace + "/" + o.Name
}

type Index struct {
	Objects []*Object
}

// ParseFile reads every object of a YAML file, after ${var} substitution.
func ParseFile(path string, vars map[string]string) ([]*Object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return parse(Substitute(data, vars), abs)
}

// ParseYAML reads every object of a YAML stream, such as a build output.
func ParseYAML(data []byte) ([]*Object, error) {
	return parse(data, "")
}

func parse(data []byte, file string) ([]*Object, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []*Object
	for i := 0; ; i++ {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if len(n.Content) == 0 || n.Content[0].Kind != yaml.MappingNode {
			continue
		}
		root := n.Content[0]
		o := &Object{
			APIVersion: Str(root, "apiVersion"),
			Kind:       Str(root, "kind"),
			Name:       Str(root, "metadata", "name"),
			Namespace:  Str(root, "metadata", "namespace"),
			File:       file,
			Doc:        i,
			Node:       root,
		}
		if o.Kind != "" {
			out = append(out, o)
		}
	}
}

// IndexRepo indexes every object of every YAML file under root, plus the
// ConfigMaps declared by configMapGenerator entries. Files that are not valid
// YAML (Helm templates, for instance) are skipped.
func IndexRepo(root string, vars map[string]string) (*Index, error) {
	ix := &Index{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "Chart.yaml")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(p); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		if isKustomizationFile(p) {
			ix.Objects = append(ix.Objects, generated(filepath.Dir(p))...)
			return nil
		}
		objs, err := ParseFile(p, vars)
		if err == nil {
			ix.Objects = append(ix.Objects, objs...)
		}
		return nil
	})
	return ix, err
}

// Find returns the object of that kind and name. An object without a
// namespace matches any namespace (kustomize may set it later). When several
// match, ConfigMaps from a generator win, then the object whose file is
// closest to near.
func (ix *Index) Find(kind, name, namespace, near string) *Object {
	var best *Object
	bestScore := -1
	for _, o := range ix.Objects {
		if o.Kind != kind || o.Name != name {
			continue
		}
		if o.Namespace != "" && namespace != "" && o.Namespace != namespace {
			continue
		}
		score := commonPrefix(filepath.Dir(o.File), filepath.Dir(near))
		if o.Namespace == namespace {
			score += 1000
		}
		if o.Gen != nil {
			score += 10000
		}
		if score > bestScore {
			best, bestScore = o, score
		}
	}
	return best
}

func commonPrefix(a, b string) int {
	if a == "" || b == "" {
		return 0
	}
	pa := strings.Split(filepath.ToSlash(a), "/")
	pb := strings.Split(filepath.ToSlash(b), "/")
	n := 0
	for n < len(pa) && n < len(pb) && pa[n] == pb[n] {
		n++
	}
	return n
}

// Get walks a mapping node by keys and returns the value node.
func Get(n *yaml.Node, path ...string) *yaml.Node {
	_, v := lookup(n, path)
	return v
}

func lookup(n *yaml.Node, path []string) (key, value *yaml.Node) {
	cur := n
	for _, k := range path {
		if cur == nil || cur.Kind != yaml.MappingNode {
			return nil, nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if cur.Content[i].Value == k {
				key, next = cur.Content[i], cur.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil, nil
		}
		cur = next
	}
	return key, cur
}

func Str(n *yaml.Node, path ...string) string {
	v := Get(n, path...)
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return v.Value
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::=([^}]*))?\}`)

// Substitute applies Flux postBuild-style ${var} and ${var:=default}
// substitution. Unknown variables without a default are left as they are.
func Substitute(data []byte, vars map[string]string) []byte {
	return varRef.ReplaceAllFunc(data, func(m []byte) []byte {
		sub := varRef.FindSubmatch(m)
		if v, ok := vars[string(sub[1])]; ok {
			return []byte(v)
		}
		if bytes.Contains(m, []byte(":=")) {
			return sub[2]
		}
		return m
	})
}
