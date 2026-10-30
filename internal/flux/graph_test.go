package flux

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const releaseYAML = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: %s
spec:
  chart: {spec: {chart: app, sourceRef: {kind: HelmRepository, name: charts}}}
`

const ks = `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: %s, namespace: flux-system}
spec:
  path: %s
`

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func rel(name string) string { return strings.Replace(releaseYAML, "%s", name, 1) }
func kust(name, path string) string {
	return strings.Replace(strings.Replace(ks, "%s", name, 1), "%s", path, 1)
}
func kdir(resources ...string) string { return "resources: [" + strings.Join(resources, ", ") + "]\n" }

func graph(t *testing.T, root, origin string) *Graph {
	t.Helper()
	ix, err := IndexRepo(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewGraph(root, ix, origin, nil)
}

// reached lists cluster/namespace/name of every HelmRelease the graph applies.
func reached(g *Graph) []string {
	var out []string
	for _, a := range g.Applied {
		for _, o := range a.Objects {
			if o.Kind == "HelmRelease" {
				out = append(out, a.Cluster+"|"+o.ID())
			}
		}
	}
	slices.Sort(out)
	return out
}

func TestGraphFollowsBootstrapKustomizationPerCluster(t *testing.T) {
	root := t.TempDir()
	cluster := func(name string) map[string]string {
		return map[string]string{
			"clusters/" + name + "/flux-system/kustomization.yaml": kdir("gotk-sync.yaml"),
			"clusters/" + name + "/flux-system/gotk-sync.yaml":     kust("flux-system", "./clusters/"+name),
			"clusters/" + name + "/apps.yaml":                      kust("apps", "./apps"),
		}
	}
	write(t, root, cluster("production"))
	write(t, root, cluster("staging"))
	write(t, root, map[string]string{"apps/kustomization.yaml": kdir("web.yaml"), "apps/web.yaml": rel("web")})

	g := graph(t, root, "")
	if !g.Explicit {
		t.Fatal("a Kustomization that applies itself is a declared root")
	}
	want := []string{"production|web", "staging|web"}
	if got := reached(g); !slices.Equal(got, want) {
		t.Errorf("reached = %v, want %v", got, want)
	}
}

func TestGraphFindsEveryClusterBesideARoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"clusters/production/flux-system/kustomization.yaml": kdir("gotk-sync.yaml"),
		"clusters/production/flux-system/gotk-sync.yaml":     kust("flux-system", "./clusters/production"),
		"clusters/production/apps.yaml":                      kust("apps", "./apps/production"),
		// A bootstrap whose kustomization.yaml leaves flux-system out still declares a cluster.
		"clusters/homelab/kustomization.yaml":             kdir("apps.yaml"),
		"clusters/homelab/apps.yaml":                      kust("apps", "./apps/homelab"),
		"clusters/homelab/flux-system/gotk-sync.yaml":     kust("flux-system", "./clusters/homelab"),
		"clusters/homelab/flux-system/kustomization.yaml": kdir("gotk-sync.yaml"),
		// A cluster without flux-system in git: orphans beside a root.
		"clusters/staging/apps.yaml":  kust("apps", "./apps/staging"),
		"clusters/staging/infra.yaml": kust("infra", "./infra"),
		"apps/production/web.yaml":    rel("web"),
		"apps/homelab/web.yaml":       rel("web"),
		"apps/staging/web.yaml":       rel("web"),
		"infra/db.yaml":               rel("db"),
		// An orphan elsewhere is an app nobody applies, not a cluster.
		"apps/old/ks.yaml":      kust("old", "./apps/old/app"),
		"apps/old/app/old.yaml": rel("old"),
	})
	g := graph(t, root, "")
	want := []string{"homelab|web", "production|web", "staging|db", "staging|web"}
	if got := reached(g); !slices.Equal(got, want) {
		t.Errorf("reached = %v, want %v", got, want)
	}
}

func TestGraphReadsFluxInstanceRoots(t *testing.T) {
	instance := func(path string) string {
		return "apiVersion: fluxcd.controlplane.io/v1\nkind: FluxInstance\nmetadata: {name: flux}\nspec:\n  sync: {kind: GitRepository, url: https://github.com/me/ops, path: " + path + "}\n"
	}
	chart := func(path string) string {
		return "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: flux-instance}\nspec:\n  chartRef: {kind: OCIRepository, name: x}\n  values:\n    instance:\n      sync: {kind: GitRepository, url: https://github.com/me/ops, path: " + path + "}\n"
	}
	tests := []struct {
		name  string
		files map[string]string
	}{
		{"object", map[string]string{"bootstrap/instance.yaml": instance("./apps")}},
		{"chart values", map[string]string{"bootstrap/flux-instance.yaml": chart("apps")}},
		{"values file", map[string]string{
			"bootstrap/kustomization.yaml": "resources: [release.yaml]\nconfigMapGenerator:\n  - {name: flux-values, files: [values.yaml=helm/values.yaml]}\n",
			"bootstrap/release.yaml":       "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: flux-instance}\nspec:\n  chartRef: {kind: OCIRepository, name: x}\n  valuesFrom:\n    - {kind: ConfigMap, name: flux-values}\n",
			"bootstrap/helm/values.yaml":   "instance:\n  sync: {kind: GitRepository, url: https://github.com/me/ops, path: apps}\n",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tt.files)
			write(t, root, map[string]string{"apps/kustomization.yaml": kdir("web.yaml"), "apps/web.yaml": rel("web")})
			g := graph(t, root, "https://github.com/me/ops.git")
			if !g.Explicit || !slices.Equal(reached(g), []string{"|web"}) {
				t.Errorf("explicit = %v, reached = %v", g.Explicit, reached(g))
			}
		})
	}
}

func TestGraphAcceptsAMirrorOfTheRepository(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"bootstrap/instance.yaml": "apiVersion: fluxcd.controlplane.io/v1\nkind: FluxInstance\nmetadata: {name: flux}\nspec:\n  sync: {kind: GitRepository, url: 'http://forgejo.forgejo.svc.cluster.local:3000/me/ops.git', path: apps}\n",
		"apps/web.yaml":           rel("web"),
	})
	if g := graph(t, root, "https://github.com/me/ops"); !g.Explicit {
		t.Error("the same owner/repository on another host is this repository")
	}
	if g := graph(t, root, "https://github.com/someone/else"); g.Explicit {
		t.Error("a FluxInstance of another repository is not a root here")
	}
}

func TestGraphLabelsClusters(t *testing.T) {
	roots := []graphRoot{{dir: "/r/kubernetes/main/flux"}, {dir: "/r/kubernetes/utility/flux"}}
	if got := clusterLabels("/r", roots); !slices.Equal(got, []string{"main", "utility"}) {
		t.Errorf("labels = %v", got)
	}
	if got := clusterLabels("/r", roots[:1]); !slices.Equal(got, []string{""}) {
		t.Errorf("one cluster needs no label, got %v", got)
	}
	nested := []graphRoot{{dir: "/r/kubernetes/flux"}, {dir: "/r/kubernetes/flux/cluster"}}
	if got := clusterLabels("/r", nested); got[0] != got[1] {
		t.Errorf("a root inside another is the same cluster, labels = %v", got)
	}
}

func TestGraphAnAppInsideItsOwnPathIsNotARoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"flux/cluster.yaml":          kust("apps", "./apps"),
		"apps/kustomization.yaml":    kdir("listed/ks.yaml"),
		"apps/listed/ks.yaml":        kust("listed", "./apps/listed/app"),
		"apps/listed/app/web.yaml":   rel("listed"),
		"apps/unlisted/ks.yaml":      kust("unlisted", "./apps/unlisted"),
		"apps/unlisted/release.yaml": rel("unlisted"),
	})
	g := graph(t, root, "")
	if g.Explicit {
		t.Error("an app whose Kustomization sits inside its own path does not declare a cluster")
	}
	if got := reached(g); !slices.Equal(got, []string{"|listed", "|unlisted"}) {
		t.Errorf("reached = %v", got)
	}
}

func TestGraphWithoutADeclaredRootStartsFromOrphans(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"flux/cluster.yaml":               kust("apps", "./apps"),
		"apps/kustomization.yaml":         kdir("web/ks.yaml"),
		"apps/web/ks.yaml":                kust("web", "./apps/web/app"),
		"apps/web/app/web.yaml":           rel("web"),
		"apps/web/app/kustomization.yaml": kdir("web.yaml"),
	})
	g := graph(t, root, "")
	if g.Explicit || g.Complete() {
		t.Errorf("explicit = %v, complete = %v: nothing is known to be not applied without a root", g.Explicit, g.Complete())
	}
	if got := reached(g); !slices.Equal(got, []string{"|web"}) {
		t.Errorf("reached = %v", got)
	}
}

func TestGraphAppliesWhatTheParentPasses(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"clusters/main/flux-system/kustomization.yaml": kdir("gotk-sync.yaml", "settings.yaml"),
		"clusters/main/flux-system/gotk-sync.yaml":     kust("flux-system", "./clusters/main"),
		"clusters/main/flux-system/settings.yaml":      "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: cluster-info, namespace: flux-system}\ndata: {CLUSTER: main}\n",
		"clusters/main/apps.yaml": `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: apps, namespace: flux-system}
spec:
  path: ./apps
  patches:
    - target: {kind: Kustomization}
      patch: |
        - op: add
          path: /spec/postBuild
          value:
            substituteFrom:
              - {kind: ConfigMap, name: cluster-info}
`,
		"clusters/other/cluster-info.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: cluster-info, namespace: flux-system}\ndata: {CLUSTER: other}\n",
		"apps/kustomization.yaml":          kdir("web.yaml"),
		"apps/web.yaml":                    strings.Replace(kust("web", "./apps/web"), "path: ./apps/web", "path: ./apps/web\n  targetNamespace: shop", 1),
		"apps/web/kustomization.yaml":      kdir("release.yaml"),
		"apps/web/release.yaml":            rel("web-${CLUSTER}"),
	})
	g := graph(t, root, "")
	if got := reached(g); !slices.Equal(got, []string{"|shop/web-main"}) {
		t.Errorf("reached = %v, want the parent's variables and the child's targetNamespace", got)
		for _, a := range g.Applied {
			t.Log(a.Dir, a.Err)
		}
	}
}

func TestGraphDoesNotClaimWhatItCannotFollow(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"clusters/main/flux-system/kustomization.yaml": kdir("gotk-sync.yaml"),
		"clusters/main/flux-system/gotk-sync.yaml":     kust("flux-system", "./clusters/main"),
	})
	if g := graph(t, root, ""); !g.Complete() {
		t.Fatal("every build worked, the graph is complete")
	}
	write(t, root, map[string]string{"clusters/main/gen.yaml": `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSet
metadata: {name: gen}
spec:
  inputs: [{app: web}]
  resources:
    - apiVersion: kustomize.toolkit.fluxcd.io/v1
      kind: Kustomization
      metadata: {name: "<< inputs.app >>"}
`})
	if g := graph(t, root, ""); g.Complete() {
		t.Error("a ResourceSet that generates Kustomizations can apply what the graph does not see")
	}
}

func TestAttributorFindsAReleaseWhoseNamespaceWasReplaced(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "release.yaml")
	write(t, root, map[string]string{"release.yaml": "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: web, namespace: monitoring}\nspec: {chart: {spec: {chart: app}}, values: {a: 1}}\n"})
	a := NewAttributor([]string{f}, nil)
	built := &Object{Kind: "HelmRelease", Name: "web", Namespace: "observability"}
	if got := a.File(built); got != f {
		t.Errorf("File = %q, want %q", got, f)
	}
	if o := a.Origins(built, []string{"a"}); len(o) != 1 {
		t.Errorf("Origins = %+v", o)
	}
}
