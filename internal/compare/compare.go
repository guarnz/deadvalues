// Package compare flattens rendered manifests into comparable snapshots.
package compare

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	releaseutil "helm.sh/helm/v4/pkg/release/v1/util"
	"sigs.k8s.io/yaml"
)

// Snapshot is a rendered release flattened to "<object id>:<field path>" -> JSON value.
// Keying by object identity instead of template file makes the comparison
// immune to resources moving between templates.
type Snapshot map[string]string

func Flatten(manifests map[string]string) (Snapshot, error) {
	files := make([]string, 0, len(manifests))
	for f := range manifests {
		files = append(files, f)
	}
	sort.Strings(files)

	snap := Snapshot{}
	for _, f := range files {
		docs := releaseutil.SplitManifests(manifests[f])
		names := make([]string, 0, len(docs))
		for n := range docs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			var obj interface{}
			if err := yaml.Unmarshal([]byte(docs[n]), &obj); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			if obj == nil {
				continue
			}
			walk(objectID(obj, f+"#"+n)+":", obj, snap)
		}
	}
	return snap, nil
}

func objectID(obj interface{}, fallback string) string {
	m, ok := obj.(map[string]interface{})
	if !ok {
		return fallback
	}
	meta, _ := m["metadata"].(map[string]interface{})
	kind, _ := m["kind"].(string)
	name, _ := meta["name"].(string)
	if kind == "" || name == "" {
		return fallback
	}
	if ns, _ := meta["namespace"].(string); ns != "" {
		return kind + "/" + ns + "/" + name
	}
	return kind + "/" + name
}

func walk(prefix string, v interface{}, snap Snapshot) {
	switch t := v.(type) {
	case map[string]interface{}:
		if len(t) == 0 {
			snap[prefix] = "{}"
		}
		for k, child := range t {
			walk(prefix+"."+k, child, snap)
		}
	case []interface{}:
		if len(t) == 0 {
			snap[prefix] = "[]"
		}
		for i, child := range t {
			walk(prefix+"["+strconv.Itoa(i)+"]", child, snap)
		}
	default:
		b, _ := json.Marshal(t)
		snap[prefix] = string(b)
	}
}

// Noise returns the paths that differ between two renders of the same input,
// e.g. randAlphaNum secrets, generated certificates and their checksum annotations.
func Noise(a, b Snapshot) map[string]bool {
	noise := map[string]bool{}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			noise[k] = true
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			noise[k] = true
		}
	}
	return noise
}

func Equal(a, b Snapshot, noise map[string]bool) bool {
	for k, v := range a {
		if noise[k] {
			continue
		}
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	for k := range b {
		if noise[k] {
			continue
		}
		if _, ok := a[k]; !ok {
			return false
		}
	}
	return true
}

// Change is one field that differs between two snapshots. An empty Before or
// After means the field is absent on that side.
type Change struct {
	Object string `json:"object"`
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

func Changes(before, after Snapshot, noise map[string]bool) []Change {
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	var out []Change
	for k := range keys {
		if noise[k] || before[k] == after[k] {
			continue
		}
		obj, field, _ := strings.Cut(k, ":")
		out = append(out, Change{Object: obj, Field: field, Before: before[k], After: after[k]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Object != out[j].Object {
			return out[i].Object < out[j].Object
		}
		return out[i].Field < out[j].Field
	})
	return out
}
