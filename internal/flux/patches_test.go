package flux

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/guarnz/deadvalues/internal/values"
)

const patchedRelease = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: web
  labels: {tier: web}
  annotations: {team: core}
spec:
  chart: {spec: {chart: app}}
  values:
    base: 1
`

// attribute builds root and returns the origins of a key of the release with that name.
func attribute(t *testing.T, root string, spec Spec, name string, path ...string) []values.Origin {
	t.Helper()
	out, err := BuildSpec(root, nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := ParseYAML(out)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAttributor(Tree(root, spec.Components...), nil)
	a.ReadPatches(root, spec)
	for _, o := range objs {
		if o.Kind == "HelmRelease" && o.Name == name {
			return a.Origins(o, path)
		}
	}
	t.Fatalf("release %s not in the build: %s", name, out)
	return nil
}

func TestOriginsOfEveryKindOfPatch(t *testing.T) {
	const merge = "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: not-used}\nspec: {values: {extra: 1}}\n"
	inline := func(s string) string {
		return "|-\n      " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n      ")
	}
	ops := "- op: add\n  path: /spec/values/extra\n  value: 1\n"
	cases := []struct {
		name     string
		kust     string
		files    map[string]string
		key      []string
		file     string
		line     int
		readOnly bool
	}{
		{
			name: "inline with a target and another name",
			kust: "resources: [release.yaml]\npatches:\n  - target: {kind: HelmRelease}\n    patch: " + inline(merge),
			key:  []string{"extra"}, file: "kustomization.yaml", line: 8, readOnly: true,
		},
		{
			name: "inline without a target",
			kust: "resources: [release.yaml]\npatches:\n  - patch: " + inline(strings.Replace(merge, "not-used", "web", 1)),
			key:  []string{"extra"}, file: "kustomization.yaml", line: 7, readOnly: true,
		},
		{
			name: "inline patchesStrategicMerge",
			kust: "resources: [release.yaml]\npatchesStrategicMerge:\n  - |-\n    " + strings.ReplaceAll(strings.TrimSpace(strings.Replace(merge, "not-used", "web", 1)), "\n", "\n    "),
			key:  []string{"extra"}, file: "kustomization.yaml", line: 7, readOnly: true,
		},
		{
			name: "inline JSON 6902",
			kust: "resources: [release.yaml]\npatches:\n  - target: {kind: HelmRelease, name: web}\n    patch: " + inline(ops),
			key:  []string{"extra"}, file: "kustomization.yaml", line: 7, readOnly: true,
		},
		{
			name: "JSON 6902 setting a map",
			kust: "resources: [release.yaml]\npatches:\n  - target: {kind: HelmRelease}\n    patch: " + inline("- op: add\n  path: /spec/values/extra\n  value:\n    nested: {a: 1}\n"),
			key:  []string{"extra", "nested", "a"}, file: "kustomization.yaml", line: 8, readOnly: true,
		},
		{
			name: "JSON 6902 inside a list",
			kust: "resources: [release.yaml, list.yaml]\npatches:\n  - target: {kind: HelmRelease, name: list}\n    patch: " + inline("- op: add\n  path: /spec/values/items/0\n  value: b\n"),
			files: map[string]string{
				"list.yaml": strings.Replace(strings.Replace(patchedRelease, "name: web", "name: list", 1), "base: 1", "items: [a]", 1),
			},
			key: []string{"items"}, file: "kustomization.yaml", line: 7, readOnly: true,
		},
		{
			name:  "JSON 6902 file",
			kust:  "resources: [release.yaml]\npatches:\n  - path: ops.yaml\n    target: {kind: HelmRelease}\n",
			files: map[string]string{"ops.yaml": ops},
			key:   []string{"extra"}, file: "ops.yaml", line: 3, readOnly: true,
		},
		{
			name: "patchesJson6902",
			kust: "resources: [release.yaml]\npatchesJson6902:\n  - target: {group: helm.toolkit.fluxcd.io, version: v2, kind: HelmRelease, name: web}\n    patch: " + inline(ops),
			key:  []string{"extra"}, file: "kustomization.yaml", line: 7, readOnly: true,
		},
		{
			name:  "file with a target and another name",
			kust:  "resources: [release.yaml]\npatches:\n  - path: patch.yaml\n    target: {kind: HelmRelease}\n",
			files: map[string]string{"patch.yaml": merge},
			key:   []string{"extra"}, file: "patch.yaml", line: 4,
		},
		{
			name: "name pattern",
			kust: "resources: [release.yaml]\npatches:\n  - target: {kind: Helm.*, name: w.+}\n    patch: " + inline(ops),
			key:  []string{"extra"}, file: "kustomization.yaml", line: 7, readOnly: true,
		},
		{
			name: "label and annotation selectors",
			kust: "resources: [release.yaml]\npatches:\n  - target: {labelSelector: tier=web, annotationSelector: team in (core)}\n    patch: " + inline(ops),
			key:  []string{"extra"}, file: "kustomization.yaml", line: 7, readOnly: true,
		},
		{
			name:  "replacement",
			kust:  "resources: [release.yaml, config.yaml]\nreplacements:\n  - source: {kind: ConfigMap, name: settings, fieldPath: data.value}\n    targets:\n      - select: {kind: HelmRelease}\n        fieldPaths: [spec.values.base]\n",
			files: map[string]string{"config.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: settings}\ndata: {value: \"2\"}\n"},
			key:   []string{"base"}, file: "kustomization.yaml", line: 6, readOnly: true,
		},
		{
			name:  "replacement in a file",
			kust:  "resources: [release.yaml, config.yaml]\nreplacements:\n  - path: replace.yaml\n",
			files: map[string]string{"config.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: settings}\ndata: {value: \"2\"}\n", "replace.yaml": "source: {kind: ConfigMap, name: settings, fieldPath: data.value}\ntargets:\n  - select: {kind: HelmRelease}\n    fieldPaths: [spec.values.base]\n"},
			key:   []string{"base"}, file: "replace.yaml", line: 4, readOnly: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{"release.yaml": patchedRelease, "kustomization.yaml": c.kust})
			write(t, root, c.files)
			name := "web"
			if strings.Contains(c.name, "list") {
				name = "list"
			}
			origins := attribute(t, root, Spec{}, name, c.key...)
			if len(origins) == 0 {
				t.Fatal("no origin")
			}
			got := origins[len(origins)-1]
			if got.File != filepath.Join(root, c.file) || got.Line != c.line || got.ReadOnly != c.readOnly || !got.IsFile {
				t.Errorf("origin = %+v, want %s:%d readOnly=%v", got, c.file, c.line, c.readOnly)
			}
		})
	}
}

func TestPatchOriginsAreOnlyWhatTheTargetSelects(t *testing.T) {
	const ops = "|-\n      - op: add\n        path: /spec/values/extra\n        value: 1\n"
	for name, target := range map[string]string{
		"name":       "{kind: HelmRelease, name: other}",
		"label":      "{labelSelector: tier=db}",
		"annotation": "{annotationSelector: team=other}",
		"kind":       "{kind: ConfigMap}",
		"group":      "{group: apps, kind: HelmRelease}",
		"version":    "{version: v1, kind: HelmRelease}",
		"namespace":  "{kind: HelmRelease, namespace: other}",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{
				"release.yaml":       patchedRelease,
				"kustomization.yaml": "resources: [release.yaml]\npatches:\n  - target: " + target + "\n    patch: " + ops,
			})
			if o := attribute(t, root, Spec{}, "web", "extra"); len(o) != 0 {
				t.Errorf("origins = %+v", o)
			}
		})
	}
}

func TestPatchOriginsStopAtTheirKustomization(t *testing.T) {
	const patch = "patches:\n  - target: {kind: HelmRelease}\n    patch: |-\n      - op: add\n        path: /spec/values/extra\n        value: 1\n"
	release := func(name string) string { return strings.Replace(patchedRelease, "name: web", "name: "+name, 1) }
	root := t.TempDir()
	write(t, root, map[string]string{
		"kustomization.yaml":           "resources: [patched, plain, included, other]\n",
		"patched/kustomization.yaml":   "resources: [release.yaml]\n" + patch,
		"patched/release.yaml":         release("patched"),
		"plain/kustomization.yaml":     "resources: [release.yaml]\n",
		"plain/release.yaml":           release("plain"),
		"included/kustomization.yaml":  "resources: [release.yaml]\ncomponents: [../component]\n",
		"included/release.yaml":        release("included"),
		"other/kustomization.yaml":     "resources: [release.yaml]\ncomponents: [../component]\n",
		"other/release.yaml":           release("other"),
		"component/kustomization.yaml": "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n" + patch,
	})
	if o := attribute(t, root, Spec{}, "patched", "extra"); len(o) != 1 {
		t.Errorf("patched: %+v", o)
	}
	if o := attribute(t, root, Spec{}, "plain", "extra"); len(o) != 0 {
		t.Errorf("plain gets the patch of a sibling: %+v", o)
	}
	for _, name := range []string{"included", "other"} {
		o := attribute(t, root, Spec{}, name, "extra")
		if len(o) != 1 || o[0].File != filepath.Join(root, "component", "kustomization.yaml") {
			t.Errorf("%s: %+v", name, o)
		}
	}
}

func TestOriginsOfFluxSpecPatches(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"app/kustomization.yaml": "resources: [release.yaml]\n",
		"app/release.yaml":       patchedRelease,
		"ks.yaml": `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: app, namespace: flux-system}
spec:
  path: ./app
  patches:
    - target: {kind: HelmRelease}
      patch: |-
        - op: add
          path: /spec/values/extra
          value: 1
`,
	})
	ix, err := IndexRepo(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := ix.Find("Kustomization", "app", "flux-system", "")
	built := *source
	built.File = ""
	a := apply(&built, filepath.Join(root, "app"), "", ix, ix, nil)
	if a.Spec.Source != source {
		t.Fatalf("Source = %+v", a.Spec.Source)
	}
	o := attribute(t, filepath.Join(root, "app"), a.Spec, "web", "extra")
	if len(o) != 1 || o[0].File != filepath.Join(root, "ks.yaml") || o[0].Line != 11 || !o[0].ReadOnly {
		t.Errorf("origins = %+v", o)
	}
}

func TestOriginsThroughNamePrefixAndSuffix(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"kustomization.yaml":      "namePrefix: dev-\nresources: [base]\n",
		"base/kustomization.yaml": "nameSuffix: -v1\nresources: [release.yaml]\npatches:\n  - target: {name: web}\n    patch: |-\n      - op: add\n        path: /spec/values/extra\n        value: 1\n",
		"base/release.yaml":       patchedRelease,
	})
	if o := attribute(t, root, Spec{}, "dev-web-v1", "base"); len(o) != 1 || o[0].File != filepath.Join(root, "base", "release.yaml") {
		t.Errorf("base: %+v", o)
	}
	if o := attribute(t, root, Spec{}, "dev-web-v1", "extra"); len(o) != 1 || o[0].Line != 8 {
		t.Errorf("extra: %+v", o)
	}
}

func TestOriginsThroughMergeKeys(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"kustomization.yaml": "resources: [release.yaml]\n",
		"release.yaml": `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: web}
spec:
  chart: {spec: {chart: app}}
  values:
    defaults: &defaults
      port: 80
    more: &more
      host: a
    app:
      <<: [*defaults, *more]
      name: x
`,
	})
	for key, line := range map[string]int{"port": 8, "host": 10, "name": 13} {
		if o := attribute(t, root, Spec{}, "web", "app", key); len(o) != 1 || o[0].Line != line {
			t.Errorf("%s: %+v, want line %d", key, o, line)
		}
	}
}

func TestPatchFilesAreNotCountedTwice(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"release.yaml":       patchedRelease,
		"patch.yaml":         "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: web}\nspec: {values: {extra: 1}}\n",
		"kustomization.yaml": "resources: [release.yaml]\npatches:\n  - path: patch.yaml\n    target: {kind: HelmRelease}\n",
	})
	if o := attribute(t, root, Spec{}, "web", "extra"); len(o) != 1 {
		t.Errorf("origins = %+v", o)
	}
}

func TestOriginsOfValuesWrittenInAConfigMap(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"config.yaml": `apiVersion: v1
kind: ConfigMap
metadata: {name: settings}
data:
  values.yaml: |
    a: 1
    b: 2
  tag: v1
`,
		"release.yaml": `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: web}
spec:
  chart: {spec: {url: oci://example.com/app}}
  valuesFrom:
    - {kind: ConfigMap, name: settings}
    - {kind: ConfigMap, name: settings, valuesKey: tag, targetPath: image.tag}
`,
	})
	ix, err := IndexRepo(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	built := *ix.Find("ConfigMap", "settings", "", "")
	built.File = ""
	r := &Resolver{RepoRoot: root, Sources: &Index{Objects: []*Object{&built}}, Fallback: ix}
	out, err := r.Resolve(ix.Find("HelmRelease", "web", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "config.yaml")
	for key, line := range map[string]int{"a": 6, "b": 7} {
		o := out.Values.Origins([]string{key})
		if len(o) != 1 || o[0].File != file || o[0].Line != line || !o[0].IsFile || !o[0].ReadOnly {
			t.Errorf("%s: %+v, want line %d", key, o, line)
		}
	}
	o := out.Values.Origins([]string{"image", "tag"})
	if len(o) != 1 || o[0].File != file || o[0].Line != 8 {
		t.Errorf("image.tag: %+v", o)
	}
}

func TestOriginsOfAKeyWrittenAsAnAlias(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"kustomization.yaml": "resources: [release.yaml]\n",
		"release.yaml": `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: &app web}
spec:
  chart: {spec: {chart: app}}
  values:
    controllers:
      *app :
        replicas: 1
`,
	})
	if o := attribute(t, root, Spec{}, "web", "controllers", "web", "replicas"); len(o) != 1 || o[0].Line != 9 {
		t.Errorf("origins = %+v", o)
	}
}

func TestOriginsOfPatchesAParentGivesAKustomization(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"clusters/main/flux-system/gotk-sync.yaml": `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata: {name: flux-system, namespace: flux-system}
spec: {url: https://example.com/repo}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: flux-system, namespace: flux-system}
spec:
  path: ./apps
  sourceRef: {kind: GitRepository, name: flux-system}
  patches:
    - target: {kind: Kustomization, labelSelector: tier=web}
      patch: |-
        apiVersion: kustomize.toolkit.fluxcd.io/v1
        kind: Kustomization
        metadata: {name: not-used}
        spec:
          patches:
            - target: {kind: HelmRelease}
              patch: |-
                apiVersion: helm.toolkit.fluxcd.io/v2
                kind: HelmRelease
                metadata: {name: not-used}
                spec:
                  values:
                    extra: 1
`,
		"apps/kustomization.yaml":  "resources: [web.yaml, plain.yaml]\n",
		"apps/web.yaml":            "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata: {name: web, namespace: flux-system, labels: {tier: web}}\nspec: {path: ./web, sourceRef: {kind: GitRepository, name: flux-system}}\n",
		"apps/plain.yaml":          "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata: {name: plain, namespace: flux-system}\nspec: {path: ./plain, sourceRef: {kind: GitRepository, name: flux-system}}\n",
		"web/kustomization.yaml":   "resources: [release.yaml]\n",
		"web/release.yaml":         patchedRelease,
		"plain/kustomization.yaml": "resources: [release.yaml]\n",
		"plain/release.yaml":       strings.Replace(patchedRelease, "name: web", "name: plain", 1),
	})
	g := graph(t, root, "")
	found := 0
	for _, a := range g.Applied {
		for _, o := range a.Objects {
			if o.Kind != "HelmRelease" {
				continue
			}
			found++
			attr := NewAttributor(Tree(a.Dir, a.Spec.Components...), a.Vars)
			attr.ReadPatches(a.Dir, a.Spec)
			got := attr.Origins(o, []string{"extra"})
			if o.Name == "plain" && len(got) != 0 {
				t.Errorf("plain gets the patch of another Kustomization: %+v", got)
			}
			if o.Name == "web" && (len(got) != 1 || got[0].Line != 27 || !strings.HasSuffix(got[0].File, "gotk-sync.yaml") || !got[0].ReadOnly) {
				t.Errorf("web: %+v", got)
			}
		}
	}
	if found != 2 {
		t.Fatalf("releases = %d", found)
	}
}

func TestThroughAnchor(t *testing.T) {
	objs, err := ParseYAML([]byte("kind: HelmRelease\nmetadata: {name: web}\nspec:\n  values:\n    defaults: &defaults\n      port: 80\n    merged:\n      <<: *defaults\n      name: x\n    alias: *defaults\n"))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		"spec.values.defaults.port": false,
		"spec.values.merged.name":   false,
		"spec.values.merged.port":   true,
		"spec.values.alias.port":    true,
	} {
		if got := throughAnchor(objs[0].Node, strings.Split(path, ".")); got != want {
			t.Errorf("%s = %v", path, got)
		}
	}
}
