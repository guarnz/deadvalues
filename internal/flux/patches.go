package flux

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/kustomize/kyaml/utils"
)

// valuePatch is a kustomize or Flux patch or replacement, with the lines of its file.
// A strategic merge patch has merge, a JSON 6902 patch or a replacement has ops.
type valuePatch struct {
	file   string
	doc    int
	target *patchTarget
	merge  *yaml.Node
	ops    []patchOp
	// readOnly is set when prune cannot remove the key from the file.
	readOnly bool
	// scope holds the files the kustomization reaches; nil is the whole build.
	scope map[string]bool
}

// patchTarget selects resources as kustomize does; empty fields match everything.
type patchTarget struct {
	group, version, kind, name, namespace *regexp.Regexp
	labels, annotations                   labels.Selector
	reject                                []*patchTarget
}

// patchOp is what a JSON 6902 operation or a replacement sets, and the line that sets it.
type patchOp struct {
	path  []string
	line  int
	value *yaml.Node
}

func anchored(pattern string) *regexp.Regexp {
	if pattern == "" {
		return nil
	}
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return nil
	}
	return re
}

func selector(s string) labels.Selector {
	sel, err := labels.Parse(s)
	if err != nil {
		return labels.Everything()
	}
	return sel
}

func readTarget(t *yaml.Node) *patchTarget {
	if t == nil {
		return nil
	}
	return &patchTarget{
		group:       anchored(Str(t, "group")),
		version:     anchored(Str(t, "version")),
		kind:        anchored(Str(t, "kind")),
		name:        anchored(Str(t, "name")),
		namespace:   anchored(Str(t, "namespace")),
		labels:      selector(Str(t, "labelSelector")),
		annotations: selector(Str(t, "annotationSelector")),
	}
}

func stringMap(n *yaml.Node) labels.Set {
	m := labels.Set{}
	if n != nil {
		_ = n.Decode(&m)
	}
	return m
}

func (t *patchTarget) selects(rel *Object, names, namespaces []string) bool {
	group, version, ok := strings.Cut(rel.APIVersion, "/")
	if !ok {
		group, version = "", group
	}
	match := func(re *regexp.Regexp, s ...string) bool {
		return re == nil || slices.ContainsFunc(s, re.MatchString)
	}
	if !match(t.group, group) || !match(t.version, version) || !match(t.kind, rel.Kind) ||
		!match(t.name, names...) || !match(t.namespace, namespaces...) ||
		!t.labels.Matches(stringMap(Get(rel.Node, "metadata", "labels"))) ||
		!t.annotations.Matches(stringMap(Get(rel.Node, "metadata", "annotations"))) {
		return false
	}
	return !slices.ContainsFunc(t.reject, func(r *patchTarget) bool { return r.selects(rel, names, namespaces) })
}

// applies reports whether the patch reaches a release, known by every name it had in the build.
func (p *valuePatch) applies(rel *Object, names, namespaces []string) bool {
	if p.target != nil {
		return p.target.selects(rel, names, namespaces)
	}
	return p.merge != nil && Str(p.merge, "kind") == rel.Kind && slices.Contains(names, Str(p.merge, "metadata", "name"))
}

// reaches reports whether the patch's kustomization includes one of the release's files.
func (p *valuePatch) reaches(docs []*Object) bool {
	if p.scope == nil || len(docs) == 0 {
		return true
	}
	return slices.ContainsFunc(docs, func(d *Object) bool { return p.scope[d.File] })
}

// line returns the line that sets a path or something inside it, 0 when the patch does not.
func (p *valuePatch) line(path []string) int {
	if p.merge != nil {
		if k, _ := lookup(p.merge, path); k != nil {
			return k.Line
		}
		return 0
	}
	for _, op := range p.ops {
		n := min(len(op.path), len(path))
		if !slices.Equal(op.path[:n], path[:n]) {
			continue
		}
		if len(path) <= len(op.path) || op.value == nil {
			return op.line
		}
		if k, _ := lookup(op.value, path[n:]); k != nil {
			return k.Line
		}
	}
	return 0
}

// parsePatch reads the documents of a patch that starts offset lines into its file.
func parsePatch(data []byte, offset int, base valuePatch) []*valuePatch {
	var out []*valuePatch
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for i := 0; ; i++ {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			return out
		}
		if len(n.Content) == 0 {
			continue
		}
		root := n.Content[0]
		moveLines(root, offset)
		p := base
		p.doc = i
		switch root.Kind {
		case yaml.MappingNode:
			p.merge = root
		case yaml.SequenceNode:
			p.ops = jsonPatchOps(root)
			p.readOnly = true
		default:
			continue
		}
		out = append(out, &p)
	}
}

func moveLines(n *yaml.Node, offset int) {
	n.Line += offset
	for _, c := range n.Content {
		moveLines(c, offset)
	}
}

// textOffset is how many lines of the file come before the text of a scalar.
func textOffset(n *yaml.Node) int {
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return n.Line
	}
	return n.Line - 1
}

func jsonPatchOps(list *yaml.Node) []patchOp {
	var out []patchOp
	for _, op := range list.Content {
		kind := Str(op, "op")
		if kind != "add" && kind != "replace" && kind != "copy" && kind != "move" {
			continue
		}
		var path []string
		for _, part := range strings.Split(strings.TrimPrefix(Str(op, "path"), "/"), "/") {
			path = append(path, strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~"))
		}
		k, v := lookup(op, []string{"value"})
		line := op.Line
		if k != nil {
			line = k.Line
		}
		out = append(out, patchOp{path: path, line: line, value: v})
	}
	return out
}

// readPatchList reads a list of patches. Files without a target are left out: the build
// reads them as documents of the release.
func readPatchList(list *yaml.Node, file, dir string, vars map[string]string) []*valuePatch {
	if list == nil {
		return nil
	}
	var out []*valuePatch
	for _, item := range list.Content {
		item = deref(item)
		text, target := item, (*patchTarget)(nil)
		if item.Kind == yaml.MappingNode {
			text, target = Get(item, "patch"), readTarget(Get(item, "target"))
			if path := Str(item, "path"); path != "" {
				if target != nil {
					out = append(out, readPatchFile(filepath.Join(dir, path), target, vars)...)
				}
				continue
			}
		}
		if text == nil || text.Kind != yaml.ScalarNode || (item == text && !strings.Contains(text.Value, "\n")) {
			continue
		}
		data := Substitute([]byte(text.Value), vars)
		out = append(out, parsePatch(data, textOffset(text), valuePatch{file: file, target: target, readOnly: true})...)
	}
	return out
}

func readPatchFile(path string, target *patchTarget, vars map[string]string) []*valuePatch {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return parsePatch(Substitute(data, vars), 0, valuePatch{file: path, target: target})
}

// readReplacements reads the targets of a list of replacements, written there or in a file.
func readReplacements(list *yaml.Node, file, dir string) []*valuePatch {
	if list == nil {
		return nil
	}
	var out []*valuePatch
	for _, item := range list.Content {
		path := Str(item, "path")
		if path == "" {
			out = append(out, replacementTargets(item, file)...)
			continue
		}
		path = filepath.Join(dir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		var doc yaml.Node
		if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
			continue
		}
		root := doc.Content[0]
		items := []*yaml.Node{root}
		if root.Kind == yaml.SequenceNode {
			items = root.Content
		}
		for _, r := range items {
			out = append(out, replacementTargets(r, path)...)
		}
	}
	return out
}

func replacementTargets(replacement *yaml.Node, file string) []*valuePatch {
	targets := Get(replacement, "targets")
	if targets == nil {
		return nil
	}
	var out []*valuePatch
	for _, t := range targets.Content {
		target := readTarget(Get(t, "select"))
		if target == nil {
			continue
		}
		if reject := Get(t, "reject"); reject != nil {
			for _, r := range reject.Content {
				target.reject = append(target.reject, readTarget(r))
			}
		}
		p := &valuePatch{file: file, target: target, readOnly: true}
		if paths := Get(t, "fieldPaths"); paths != nil {
			for _, f := range paths.Content {
				p.ops = append(p.ops, patchOp{path: utils.SmarterPathSplitter(f.Value, "."), line: f.Line})
			}
		}
		out = append(out, p)
	}
	return out
}

// kustomizationPatches reads what the kustomization of dir does to releases.
func (a *Attributor) kustomizationPatches(dir string) []*valuePatch {
	for _, name := range kustomizationNames {
		file := filepath.Join(dir, name)
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if abs, err := filepath.Abs(file); err == nil {
			file = abs
		}
		var doc yaml.Node
		if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
			return nil
		}
		root := doc.Content[0]
		if prefix, suffix := Str(root, "namePrefix"), Str(root, "nameSuffix"); prefix != "" || suffix != "" {
			a.affixes = append(a.affixes, [2]string{prefix, suffix})
		}
		var out []*valuePatch
		for _, field := range []string{"patches", "patchesStrategicMerge", "patchesJson6902"} {
			out = append(out, readPatchList(Get(root, field), file, dir, a.vars)...)
		}
		return append(out, readReplacements(Get(root, "replacements"), file, dir)...)
	}
	return nil
}

// ReadPatches adds the patches of the build of dir: those of its kustomizations, which
// reach only what they include, and those of the Flux Kustomization, which reach it all.
func (a *Attributor) ReadPatches(dir string, spec Spec) {
	top := filepath.Clean(dir)
	seen := map[string]bool{}
	scopes := map[string]map[string]bool{}
	scope := func(owner string) map[string]bool {
		if s, ok := scopes[owner]; ok {
			return s
		}
		s := map[string]bool{}
		var components []string
		if owner == top {
			components = spec.Components
		}
		for _, f := range Tree(owner, components...) {
			if abs, err := filepath.Abs(f); err == nil {
				s[abs] = true
			}
		}
		scopes[owner] = s
		return s
	}
	var walk func(dir, owner string, extra []string)
	walk = func(dir, owner string, extra []string) {
		dir = filepath.Clean(dir)
		if seen[dir+"\x00"+owner] {
			return
		}
		seen[dir+"\x00"+owner] = true
		for _, p := range a.kustomizationPatches(dir) {
			p.scope = scope(owner)
			a.patches = append(a.patches, p)
		}
		k, ok := readKustomization(dir)
		if !ok && dir == top {
			k, ok = pathKustomization(dir), true
		}
		if !ok {
			return
		}
		local := func(r string) (string, bool) {
			p := filepath.Join(dir, r)
			info, err := os.Stat(p)
			return p, err == nil && info.IsDir() && !isRemote(r)
		}
		for _, r := range k.Resources {
			if p, ok := local(r); ok {
				walk(p, p, nil)
			}
		}
		for _, c := range slices.Concat(k.Components, extra) {
			if p, ok := local(c); ok {
				walk(p, owner, nil)
			}
		}
	}
	walk(top, top, spec.Components)
	if spec.Source != nil {
		a.patches = append(a.patches, readPatchList(Get(spec.Source.Node, "spec", "patches"), spec.Source.File, top, a.vars)...)
	}
	a.patches = append(a.patches, spec.inherited...)
}

// childPatches reads the spec.patches a Kustomization gets from the patches of the build
// that applies it, as when a cluster Kustomization sets defaults on its children.
func (a *Attributor) childPatches(ks *Object, dir string) []*valuePatch {
	var out []*valuePatch
	for _, p := range a.patches {
		if p.merge != nil && p.applies(ks, a.names(ks), []string{ks.Namespace}) {
			out = append(out, readPatchList(Get(p.merge, "spec", "patches"), p.file, dir, a.vars)...)
		}
	}
	return out
}
