package flux

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/guarnz/deadvalues/internal/values"
)

// fixture copies testdata/flux and the synthetic chart into a temp repo.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyTree(t, "../../testdata/flux", root)
	copyTree(t, "../../testdata/charts/cases", filepath.Join(root, "charts", "cases"))
	return root
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
}

func release(t *testing.T, objs []*Object) *Object {
	t.Helper()
	for _, o := range objs {
		if IsRelease(o) {
			return o
		}
	}
	t.Fatal("no HelmRelease")
	return nil
}

func TestResolveStandaloneFile(t *testing.T) {
	root := fixture(t)
	ix, err := IndexRepo(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := ParseFile(filepath.Join(root, "infrastructure", "standalone.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &Resolver{RepoRoot: root, Sources: ix}
	out, err := r.Resolve(release(t, objs))
	if err != nil {
		t.Fatal(err)
	}

	if out.ID != "infra/standalone" || out.ReleaseName != "web-standalone" || out.Namespace != "web" {
		t.Errorf("identity = %s %s %s", out.ID, out.ReleaseName, out.Namespace)
	}
	if out.Chart.Chart != filepath.Join(root, "charts", "cases") || !out.Chart.IsLocal() {
		t.Errorf("chart = %+v", out.Chart)
	}
	if tag, _ := values.Lookup(out.Values.Values, []string{"image", "tag"}); tag != "1.29" {
		t.Errorf("image.tag from valuesFrom targetPath = %v", tag)
	}
	if rep, _ := values.Lookup(out.Values.Values, []string{"replicas"}); rep != float64(2) {
		t.Errorf("replicas = %#v", rep)
	}
	if len(out.Notes) != 1 || !strings.Contains(out.Notes[0], "SOPS") {
		t.Errorf("notes = %v", out.Notes)
	}

	o := out.Values.ExactOrigins([]string{"service", "prot"})
	if len(o) != 1 || !o[0].IsFile || o[0].Doc != 3 || o[0].Line != 56 || strings.Join(o[0].Prefix, ".") != "spec.values" {
		t.Errorf("service.prot origin = %+v", o)
	}
}

func TestResolveSourceKinds(t *testing.T) {
	objs, err := ParseYAML([]byte(`apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata: {name: http, namespace: ns}
spec: {url: https://charts.example.com}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata: {name: oci, namespace: ns}
spec: {type: oci, url: oci://ghcr.io/org/charts}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata: {name: tagged, namespace: ns}
spec: {url: oci://quay.io/jetstack/charts/cert-manager, ref: {semver: "1.x"}}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata: {name: digest, namespace: ns}
spec: {url: oci://quay.io/x/y, ref: {digest: "sha256:abc"}}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmChart
metadata: {name: hc, namespace: ns}
spec: {chart: podinfo, version: "6.5.0", sourceRef: {kind: HelmRepository, name: http}}
`))
	if err != nil {
		t.Fatal(err)
	}
	r := &Resolver{Sources: &Index{Objects: objs}}
	rel := func(spec string) *Object {
		o, err := ParseYAML([]byte("apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: app, namespace: ns}\nspec:\n" + spec))
		if err != nil {
			t.Fatal(err)
		}
		return o[0]
	}

	tests := []struct {
		spec, repo, chart, version, err string
	}{
		{"  chart: {spec: {chart: podinfo, version: '>=6.0.0', sourceRef: {kind: HelmRepository, name: http}}}", "https://charts.example.com", "podinfo", ">=6.0.0", ""},
		{"  chart: {spec: {chart: app, sourceRef: {kind: HelmRepository, name: oci}}}", "oci://ghcr.io/org/charts", "app", "*", ""},
		{"  chartRef: {kind: OCIRepository, name: tagged}", "", "oci://quay.io/jetstack/charts/cert-manager", "1.x", ""},
		{"  chartRef: {kind: HelmChart, name: hc}", "https://charts.example.com", "podinfo", "6.5.0", ""},
		{"  chartRef: {kind: OCIRepository, name: digest}", "", "", "", "pinned by digest"},
		{"  chartRef: {kind: OCIRepository, name: nope}", "", "", "", "not found"},
	}
	for _, tt := range tests {
		out, err := r.Resolve(rel(tt.spec))
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("%s: err = %v, want %q", tt.spec, err, tt.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tt.spec, err)
			continue
		}
		if out.Chart.Repo != tt.repo || out.Chart.Chart != tt.chart || out.Chart.Version != tt.version {
			t.Errorf("%s: ref = %+v", tt.spec, out.Chart)
		}
	}
}

func TestValuesFromNotInRepository(t *testing.T) {
	repo, err := ParseYAML([]byte("apiVersion: source.toolkit.fluxcd.io/v1\nkind: HelmRepository\nmetadata: {name: http, namespace: ns}\nspec: {url: https://charts.example.com}\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Resolver{Sources: &Index{Objects: repo}}
	objs, err := ParseYAML([]byte(`apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: tempo, namespace: ns}
spec:
  chart: {spec: {chart: tempo, sourceRef: {kind: HelmRepository, name: http}}}
  valuesFrom:
    - {kind: Secret, name: tempo-s3, valuesKey: bucket, targetPath: storage.trace.s3.bucket}
    - {kind: Secret, name: tempo-s3, valuesKey: endpoint, targetPath: storage.trace.s3.endpoint}
    - {kind: Secret, name: tempo-extra}
    - {kind: ConfigMap, name: tuning, optional: true}
  values:
    replicas: 2
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Resolve(objs[0])
	if err != nil {
		t.Fatalf("a valuesFrom source missing from the repository should not fail the release: %v", err)
	}
	if got := out.Placeholders; len(got) != 2 || strings.Join(got[0], ".") != "storage.trace.s3.bucket" || strings.Join(got[1], ".") != "storage.trace.s3.endpoint" {
		t.Errorf("placeholders = %v, want the bucket and endpoint paths", got)
	}
	if len(out.Notes) != 2 {
		t.Errorf("notes = %q, want one per missing source", out.Notes)
	}
	notes := strings.Join(out.Notes, "\n")
	for _, want := range []string{"Secret ns/tempo-s3 are not in the repository; placeholders at storage.trace.s3.bucket, storage.trace.s3.endpoint", "Secret ns/tempo-extra are not in the repository and were not checked"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes = %q, want %q", notes, want)
		}
	}
	if strings.Contains(notes, "tuning") {
		t.Errorf("an optional source missing from the repository should be skipped silently: %q", notes)
	}
	if v, _ := values.Lookup(out.Values.Values, []string{"replicas"}); v != float64(2) {
		t.Errorf("replicas = %#v", v)
	}
}

func TestBuildAddsFluxComponents(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"app/kustomization.yaml":              "resources:\n  - base.yaml\npatches:\n  - path: patch.yaml\n",
		"app/base.yaml":                       "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: base\n",
		"app/patch.yaml":                      "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: extra\ndata:\n  patched: \"true\"\n",
		"components/extra/kustomization.yaml": "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - extra.yaml\n",
		"components/extra/extra.yaml":         "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: extra\n",
	}
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	app := filepath.Join(root, "app")
	// Through a symlink, as macOS temp dirs are, the build must still find the overlay.
	if link := filepath.Join(t.TempDir(), "link"); os.Symlink(root, link) == nil {
		app = filepath.Join(link, "app")
	}

	if _, err := Build(app, nil); err == nil {
		t.Fatal("without the component the patch has no target and the build should fail")
	}
	out, err := Build(app, nil, "../components/extra")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "patched: \"true\"") {
		t.Errorf("the component's ConfigMap should be patched:\n%s", out)
	}
	if tree := strings.Join(Tree(app, "../components/extra"), "\n"); !strings.Contains(tree, filepath.Join("components", "extra", "extra.yaml")) {
		t.Errorf("Tree should list the component's files:\n%s", tree)
	}
	if data, _ := os.ReadFile(filepath.Join(app, "kustomization.yaml")); strings.Contains(string(data), "components") {
		t.Error("the kustomization file on disk was changed")
	}
}

func TestKustomizationFor(t *testing.T) {
	root := t.TempDir()
	objs, err := ParseYAML([]byte(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: cilium, namespace: flux-system}
spec: {path: ./apps/kube-system/cilium/app, targetNamespace: kube-system}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: apps, namespace: flux-system}
spec: {path: ./apps}
---
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
metadata: {name: not-flux}
spec: {path: ./apps/default/echo}
`))
	if err != nil {
		t.Fatal(err)
	}
	ix := &Index{Objects: objs}
	for _, tt := range []struct{ dir, want string }{
		{"apps/kube-system/cilium/app", "cilium"},
		{"apps/kube-system/cilium/app/config", "cilium"},
		{"apps/default/echo", "apps"},
		{"other", ""},
	} {
		got := ""
		if ks := ix.KustomizationFor(root, filepath.Join(root, tt.dir)); ks != nil {
			got = ks.Name
		}
		if got != tt.want {
			t.Errorf("KustomizationFor(%s) = %q, want %q", tt.dir, got, tt.want)
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, filepath.Join(root, "apps", "kube-system", "cilium", "app"))
	if err != nil {
		t.Skip("no relative path to the temp dir on this system")
	}
	if ks := ix.KustomizationFor(root, rel); ks == nil || ks.Name != "cilium" {
		t.Errorf("a relative dir, as --app passes it, should find the cilium Kustomization: %v", ks)
	}
}

func TestKustomizationVars(t *testing.T) {
	objs, err := ParseYAML([]byte(`apiVersion: v1
kind: ConfigMap
metadata: {name: cluster-settings, namespace: flux-system}
data: {CLUSTER: main, REGION: eu}
---
apiVersion: v1
kind: Secret
metadata: {name: cluster-secrets, namespace: flux-system}
stringData: {DOMAIN: example.com}
data: {TOKEN: c2VjcmV0}
---
apiVersion: v1
kind: Secret
metadata: {name: encrypted, namespace: flux-system}
stringData: {KEY: "ENC[AES256_GCM,data:abc]"}
sops: {version: 3.9.0}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: app, namespace: flux-system}
spec:
  path: ./apps/app
  postBuild:
    substitute: {APP: web, REGION: us}
    substituteFrom:
      - {kind: ConfigMap, name: cluster-settings}
      - {kind: Secret, name: cluster-secrets}
      - {kind: Secret, name: absent}
      - {kind: ConfigMap, name: also-absent, optional: true}
      - {kind: Secret, name: encrypted}
`))
	if err != nil {
		t.Fatal(err)
	}
	ix := &Index{Objects: objs}
	vars, notes := ix.Vars(objs[3])
	want := map[string]string{"APP": "web", "CLUSTER": "main", "REGION": "us", "DOMAIN": "example.com", "TOKEN": "secret"}
	if !reflect.DeepEqual(vars, want) {
		t.Errorf("vars = %v, want %v", vars, want)
	}
	all := strings.Join(notes, "\n")
	if len(notes) != 2 || !strings.Contains(all, "Secret absent are not in the repository") || !strings.Contains(all, "Secret encrypted are SOPS-encrypted") {
		t.Errorf("notes = %q, want the absent and the encrypted Secret only", notes)
	}
}

func TestKustomizationAliases(t *testing.T) {
	root := t.TempDir()
	objs, err := ParseYAML([]byte(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: &app ollama
  namespace: &namespace ai
spec:
  targetNamespace: *namespace
  path: ./apps/ai/ollama/app
  postBuild:
    substitute:
      APP: *app
`))
	if err != nil {
		t.Fatal(err)
	}
	ix := &Index{Objects: objs}
	ks := ix.KustomizationFor(root, filepath.Join(root, "apps", "ai", "ollama", "app"))
	if ks == nil {
		t.Fatal("no Kustomization found for the app")
	}
	if got := Str(ks.Node, "spec", "targetNamespace"); got != "ai" {
		t.Errorf("targetNamespace = %q, want the anchored value ai", got)
	}
	if vars, _ := ix.Vars(ks); vars["APP"] != "ollama" {
		t.Errorf("APP = %q, want the anchored value ollama", vars["APP"])
	}
}

func TestResolveAnchorsOutsideValues(t *testing.T) {
	repo, err := ParseYAML([]byte("apiVersion: source.toolkit.fluxcd.io/v1\nkind: HelmRepository\nmetadata: {name: http, namespace: ns}\nspec: {url: https://charts.example.com}\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Resolver{Sources: &Index{Objects: repo}}
	doc := []byte(`apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: &app web
  namespace: ns
spec:
  chart: {spec: {chart: podinfo, sourceRef: {kind: HelmRepository, name: http}}}
  values:
    fullnameOverride: *app
    labels: &labels {team: web}
    podLabels: *labels
`)
	file := filepath.Join(t.TempDir(), "helm-release.yaml")
	if err := os.WriteFile(file, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	fromFile, err := ParseFile(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	fromBuild, err := ParseYAML(doc)
	if err != nil {
		t.Fatal(err)
	}

	for name, objs := range map[string][]*Object{"file": fromFile, "build": fromBuild} {
		out, err := r.Resolve(release(t, objs))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if v, _ := values.Lookup(out.Values.Values, []string{"fullnameOverride"}); v != "web" {
			t.Errorf("%s: fullnameOverride = %#v", name, v)
		}
		if v, _ := values.Lookup(out.Values.Values, []string{"podLabels", "team"}); v != "web" {
			t.Errorf("%s: podLabels.team = %#v", name, v)
		}
	}
}

func TestRootsLeaveOutComponents(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("apps/web/kustomization.yaml", "resources:\n  - release.yaml\n")
	write("apps/web/release.yaml", "kind: ConfigMap\n")
	write("components/sidecar/kustomization.yaml", "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - release.yaml\n")
	write("components/sidecar/release.yaml", "kind: ConfigMap\n")

	roots, err := Roots(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(root, "apps", "web")}; !reflect.DeepEqual(roots, want) {
		t.Errorf("Roots = %v, want only the overlay %v", roots, want)
	}
	components, err := Components(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(root, "components", "sidecar")}; !reflect.DeepEqual(components, want) {
		t.Errorf("Components = %v, want %v", components, want)
	}
	direct := filepath.Join(root, "components", "sidecar")
	if roots, _ := Roots(direct); !reflect.DeepEqual(roots, []string{direct}) {
		t.Errorf("a component given as the directory to scan is still built: Roots = %v", roots)
	}
}

func TestRootsAndOverlayBuild(t *testing.T) {
	root := fixture(t)
	roots, err := Roots(filepath.Join(root, "apps"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range roots {
		names = append(names, filepath.Base(r))
	}
	if strings.Join(names, ",") != "production,staging" {
		t.Fatalf("roots = %v", names)
	}

	prod := filepath.Join(root, "apps", "production")
	out, err := Build(prod, nil)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := ParseYAML(out)
	if err != nil {
		t.Fatal(err)
	}
	attr := NewAttributor(Tree(prod), nil)
	r := &Resolver{
		RepoRoot:  root,
		Sources:   &Index{Objects: append(TreeGenerators(prod), objs...)},
		Attribute: attr.Origins,
	}
	rel, err := r.Resolve(release(t, objs))
	if err != nil {
		t.Fatal(err)
	}
	if rel.ID != "demo/demo" {
		t.Errorf("id = %s", rel.ID)
	}
	get := func(p ...string) interface{} { v, _ := values.Lookup(rel.Values.Values, p); return v }
	if get("image", "tag") != "1.28" || get("replicas") != float64(3) || get("ingress", "host") != "demo.example.com" {
		t.Errorf("values = %v", rel.Values.Values)
	}

	origin := func(p ...string) string {
		o := rel.Values.Origins(p)
		if len(o) == 0 {
			return ""
		}
		last := o[len(o)-1]
		rel, _ := filepath.Rel(root, last.File)
		return filepath.ToSlash(rel) + ":" + strconv.Itoa(last.Line)
	}
	if got := origin("image", "tag"); got != "apps/production/demo-patch.yaml:9" {
		t.Errorf("image.tag origin = %s", got)
	}
	if got := origin("service", "prot"); got != "apps/base/demo/release.yaml:19" {
		t.Errorf("service.prot origin = %s", got)
	}
	if got := origin("replicas"); got != "apps/base/demo/demo-values.yaml:1" {
		t.Errorf("replicas origin = %s", got)
	}
	if attr.File(release(t, objs)) != filepath.Join(root, "apps", "base", "demo", "release.yaml") {
		t.Errorf("release file = %s", attr.File(release(t, objs)))
	}
}

func TestSubstitute(t *testing.T) {
	in := "a: ${domain}\nb: ${missing}\nc: ${port:=8080}\nd: ${domain:=x}\n"
	got := string(Substitute([]byte(in), map[string]string{"domain": "example.com"}))
	want := "a: example.com\nb: ${missing}\nc: 8080\nd: example.com\n"
	if got != want {
		t.Errorf("got %q", got)
	}
}
