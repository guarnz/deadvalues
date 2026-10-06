package flux

import (
	"os"
	"path/filepath"
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
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
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
