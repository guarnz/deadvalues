// Package values merges values the way Helm and Argo CD do and remembers
// where every key came from, so findings can point at a file and line.
package values

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/strvals"
)

// Origin is where a key was set: a values file and line, or a synthetic
// source such as "--set" or an Application's inline values. For values
// embedded in a manifest (a HelmRelease's spec.values), Doc is the YAML
// document inside File and Prefix the path of the values block in it.
type Origin struct {
	File   string   `json:"file"`
	Line   int      `json:"line,omitempty"`
	IsFile bool     `json:"-"`
	Doc    int      `json:"-"`
	Prefix []string `json:"-"`
}

type Set struct {
	Values  map[string]interface{}
	Sources []string
	// manifests are sources that hold values inside a bigger manifest (a
	// HelmRelease's spec.values) rather than being values files.
	manifests map[string]bool
	origins   map[string][]Origin
}

func New() *Set {
	return &Set{Values: map[string]interface{}{}, origins: map[string][]Origin{}, manifests: map[string]bool{}}
}

// ValuesFiles returns the sources that are plain values files: files that
// several apps can load as a whole, unlike values embedded in a manifest.
func (s *Set) ValuesFiles() []string {
	var out []string
	for _, src := range s.Sources {
		if !s.manifests[src] {
			out = append(out, src)
		}
	}
	return out
}

func Key(path []string) string { return strings.Join(path, "\x00") }

func Display(path []string) string { return strings.Join(path, ".") }

func (s *Set) AddFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if err := s.add(data, Origin{File: abs, IsFile: true}); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	s.addSource(abs)
	return nil
}

func (s *Set) AddYAML(data []byte, origin string) error {
	if err := s.add(data, Origin{File: origin}); err != nil {
		return fmt.Errorf("%s: %w", origin, err)
	}
	s.Sources = append(s.Sources, origin)
	return nil
}

func (s *Set) AddMap(m map[string]interface{}, origin string) {
	s.track(m, nil, Origin{File: origin}, nil)
	s.Values = Merge(s.Values, m)
	s.Sources = append(s.Sources, origin)
}

// AddSet applies a `--set` style expression on top of the current values.
func (s *Set) AddSet(expr, origin string, forceString bool) error {
	m := map[string]interface{}{}
	parse := strvals.ParseInto
	if forceString {
		parse = strvals.ParseIntoString
	}
	if err := parse(expr, m); err != nil {
		return fmt.Errorf("%s %q: %w", origin, expr, err)
	}
	s.track(m, nil, Origin{File: origin}, nil)
	s.Values = Merge(s.Values, m)
	return nil
}

// AddNode merges a values block embedded in a manifest, such as a
// HelmRelease's spec.values. n must come from parsing the whole file so its
// line numbers are the file's.
func (s *Set) AddNode(n *yaml.Node, file string, doc int, prefix []string) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(n); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	vals, err := common.ReadValues(buf.Bytes())
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	lines := map[string]int{}
	collectLines(n, nil, lines)
	origin := Origin{File: absPath(file), IsFile: true, Doc: doc, Prefix: prefix}
	m := vals.AsMap()
	s.track(m, nil, origin, lines)
	s.Values = Merge(s.Values, m)
	s.addSource(origin.File)
	s.manifests[origin.File] = true
	return nil
}

// AddAttributed merges m with origins computed per key, for values whose
// final content comes from a kustomize build but whose keys can be traced
// back to the files that define them.
func (s *Set) AddAttributed(m map[string]interface{}, label string, attribute func(path []string) []Origin) {
	var walk func(m map[string]interface{}, path []string)
	walk = func(m map[string]interface{}, path []string) {
		for k, v := range m {
			p := append(append([]string{}, path...), k)
			origins := attribute(p)
			if len(origins) == 0 {
				origins = []Origin{{File: label}}
			}
			s.origins[Key(p)] = append(s.origins[Key(p)], origins...)
			for _, o := range origins {
				if o.IsFile {
					s.addSource(o.File)
					if o.Prefix != nil {
						s.manifests[o.File] = true
					}
				}
			}
			if child, ok := v.(map[string]interface{}); ok {
				walk(child, p)
			}
		}
	}
	walk(m, nil)
	s.Values = Merge(s.Values, m)
}

// AddAt places data's values under path, like Flux valuesFrom targetPath.
func (s *Set) AddAt(path []string, v interface{}, origin Origin) {
	m := map[string]interface{}{}
	SetPath(m, path, v)
	s.track(m, nil, origin, nil)
	s.Values = Merge(s.Values, m)
	if origin.IsFile {
		s.addSource(origin.File)
	}
}

func (s *Set) addSource(src string) {
	for _, x := range s.Sources {
		if x == src {
			return
		}
	}
	s.Sources = append(s.Sources, src)
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func (s *Set) add(data []byte, origin Origin) error {
	vals, err := common.ReadValues(data)
	if err != nil {
		return err
	}
	lines := map[string]int{}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err == nil {
		collectLines(&doc, nil, lines)
	}
	m := vals.AsMap()
	s.track(m, nil, origin, lines)
	s.Values = Merge(s.Values, m)
	return nil
}

func (s *Set) track(m map[string]interface{}, path []string, origin Origin, lines map[string]int) {
	for k, v := range m {
		p := append(append([]string{}, path...), k)
		o := origin
		o.Line = lines[Key(p)]
		s.origins[Key(p)] = append(s.origins[Key(p)], o)
		if child, ok := v.(map[string]interface{}); ok {
			s.track(child, p, origin, lines)
		}
	}
}

// Origins returns every place that set path, falling back to the closest
// ancestor when the exact key was set as part of a larger value.
func (s *Set) Origins(path []string) []Origin {
	for i := len(path); i > 0; i-- {
		if o, ok := s.origins[Key(path[:i])]; ok {
			return o
		}
	}
	return nil
}

// ExactOrigins returns only the places that set path itself.
func (s *Set) ExactOrigins(path []string) []Origin {
	return s.origins[Key(path)]
}

func collectLines(n *yaml.Node, path []string, lines map[string]int) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			collectLines(c, path, lines)
		}
	case yaml.AliasNode:
		if n.Alias != nil {
			collectLines(n.Alias, path, lines)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Value == "<<" {
				continue
			}
			p := append(append([]string{}, path...), k.Value)
			lines[Key(p)] = k.Line
			collectLines(v, p, lines)
		}
	}
}

// Merge deep-merges b over a, the same way Helm merges -f files.
func Merge(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if vm, ok := v.(map[string]interface{}); ok {
			if am, ok := out[k].(map[string]interface{}); ok {
				out[k] = Merge(am, vm)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func Lookup(m map[string]interface{}, path []string) (interface{}, bool) {
	var cur interface{} = m
	for _, k := range path {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur, ok = mm[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func Delete(m map[string]interface{}, path []string) {
	for i, k := range path {
		if i == len(path)-1 {
			delete(m, k)
			return
		}
		next, ok := m[k].(map[string]interface{})
		if !ok {
			return
		}
		m = next
	}
}

func SetPath(m map[string]interface{}, path []string, v interface{}) {
	for i, k := range path {
		if i == len(path)-1 {
			m[k] = v
			return
		}
		next, ok := m[k].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			m[k] = next
		}
		m = next
	}
}

func Copy(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, c := range t {
			out[k] = Copy(c)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, c := range t {
			out[i] = Copy(c)
		}
		return out
	default:
		return t
	}
}

func CopyMap(m map[string]interface{}) map[string]interface{} {
	return Copy(m).(map[string]interface{})
}

// Leaves calls fn for every leaf: scalars, lists, nulls and empty maps.
func Leaves(m map[string]interface{}, path []string, fn func(path []string, v interface{})) {
	for k, v := range m {
		p := append(append([]string{}, path...), k)
		if child, ok := v.(map[string]interface{}); ok && len(child) > 0 {
			Leaves(child, p, fn)
			continue
		}
		fn(p, v)
	}
}

// Resolve turns a dotted key typed by a user into a path inside m. Keys can
// contain dots themselves (e.g. label names), so segments are joined greedily
// until they match an existing key.
func Resolve(m map[string]interface{}, dotted string) ([]string, bool) {
	return resolve(m, strings.Split(dotted, "."))
}

func resolve(m map[string]interface{}, parts []string) ([]string, bool) {
	if len(parts) == 0 {
		return nil, true
	}
	for i := len(parts); i > 0; i-- {
		key := strings.Join(parts[:i], ".")
		v, ok := m[key]
		if !ok {
			continue
		}
		if i == len(parts) {
			return []string{key}, true
		}
		child, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if rest, ok := resolve(child, parts[i:]); ok {
			return append([]string{key}, rest...), true
		}
	}
	return nil, false
}
