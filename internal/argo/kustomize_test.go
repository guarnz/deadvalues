package argo

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func kustomizeApp(t *testing.T, root, path, extra string) *Application {
	t.Helper()
	apps, err := Parse([]byte(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  source:
    repoURL: https://github.com/acme/apps
    path: ` + path + "\n" + extra))
	if err != nil {
		t.Fatal(err)
	}
	return apps[0]
}

// layerKinds describes the layers as "file:<name>" or "inline:<line>".
func layerKinds(ls []Layer) []string {
	var out []string
	for _, l := range ls {
		if l.Node != nil {
			out = append(out, "inline:"+filepath.Base(l.File)+":"+strings.Join(l.Prefix, "."))
			continue
		}
		out = append(out, "file:"+filepath.Base(l.File))
	}
	return out
}

func TestKustomizeHelmChartsBecomeTargets(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "apps", "web", "kustomization.yaml"), `helmGlobals:
  chartHome: ../../charts
helmCharts:
  - name: podinfo
    repo: https://stefanprodan.github.io/podinfo
    version: 6.7.0
    releaseName: podinfo-web
    namespace: web
    valuesFile: values.yaml
    additionalValuesFiles:
      - prod.yaml
    apiVersions:
      - monitoring.coreos.com/v1
  - name: local
    valuesInline:
      replicas: 3
`)
	targets, err := kustomizeApp(t, root, "apps/web", "").Targets(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2: %+v", len(targets), targets)
	}
	repo, local := targets[0], targets[1]
	if repo.Name != "web/podinfo-web" || repo.Release != "podinfo-web" || repo.Namespace != "web" {
		t.Errorf("repo chart: name %q release %q namespace %q", repo.Name, repo.Release, repo.Namespace)
	}
	if repo.Chart.Repo != "https://stefanprodan.github.io/podinfo" || repo.Chart.Chart != "podinfo" || repo.Chart.Version != "6.7.0" {
		t.Errorf("repo chart ref: %+v", repo.Chart)
	}
	if got, want := layerKinds(repo.Layers), []string{"file:values.yaml", "file:prod.yaml"}; !reflect.DeepEqual(got, want) {
		t.Errorf("repo layers %v, want %v", got, want)
	}
	if !reflect.DeepEqual(repo.APIVersions, []string{"monitoring.coreos.com/v1"}) {
		t.Errorf("apiVersions %v", repo.APIVersions)
	}
	if local.Name != "web/local" || local.Release != "local" || local.Namespace != "" {
		t.Errorf("local chart: name %q release %q namespace %q, want the chart name as release and no namespace", local.Name, local.Release, local.Namespace)
	}
	if want := filepath.Join(root, "charts", "local"); local.Chart.Chart != want || local.Chart.Repo != "" {
		t.Errorf("local chart ref %+v, want %s under chartHome", local.Chart, want)
	}
	if got, want := layerKinds(local.Layers), []string{"inline:kustomization.yaml:helmCharts.1.valuesInline"}; !reflect.DeepEqual(got, want) {
		t.Errorf("local layers %v, want %v", got, want)
	}
	if n := local.Layers[0].Node; n.Line != 16 {
		t.Errorf("valuesInline node at line %d, want 16 so keys point at the kustomization", n.Line)
	}
}

func TestKustomizeValuesMerge(t *testing.T) {
	for _, tt := range []struct {
		merge, file string
		inline      bool
		want        []string
		err         bool
	}{
		{"", "values.yaml", true, []string{"file:values.yaml", "inline"}, false},
		{"override", "values.yaml", true, []string{"file:values.yaml", "inline"}, false},
		{"merge", "values.yaml", true, []string{"inline", "file:values.yaml"}, false},
		{"replace", "values.yaml", true, []string{"inline"}, false},
		{"replace", "values.yaml", false, []string{"file:values.yaml"}, false},
		{"", "", true, []string{"inline"}, false},
		{"", "", false, nil, false},
		{"append", "values.yaml", true, nil, true},
	} {
		root := t.TempDir()
		body := "helmCharts:\n  - name: web\n    repo: https://charts.example.com\n"
		if tt.merge != "" {
			body += "    valuesMerge: " + tt.merge + "\n"
		}
		if tt.file != "" {
			body += "    valuesFile: " + tt.file + "\n"
		}
		if tt.inline {
			body += "    valuesInline:\n      replicas: 2\n"
		}
		write(t, filepath.Join(root, "app", "kustomization.yaml"), body)
		targets, err := kustomizeApp(t, root, "app", "").Targets(root)
		if tt.err {
			if err == nil || !strings.Contains(err.Error(), "valuesMerge") {
				t.Errorf("valuesMerge %q: err %v, want a valuesMerge error", tt.merge, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("valuesMerge %q: %v", tt.merge, err)
		}
		var got []string
		for _, k := range layerKinds(targets[0].Layers) {
			if strings.HasPrefix(k, "inline:") {
				k = "inline"
			}
			got = append(got, k)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("valuesMerge %q, file %q, inline %v: layers %v, want %v", tt.merge, tt.file, tt.inline, got, tt.want)
		}
	}
}

func TestKustomizeBasesComponentsAndPatches(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "base", "kustomization.yaml"), "helmCharts:\n  - name: base-chart\n    repo: https://charts.example.com\n")
	write(t, filepath.Join(root, "comp", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nhelmCharts:\n  - name: comp-chart\n    repo: https://charts.example.com\n")
	write(t, filepath.Join(root, "remote", "kustomization.yaml"), "resources:\n  - https://github.com/acme/other//base?ref=v1\n")
	write(t, filepath.Join(root, "overlay", "kustomization.yaml"), "resources:\n  - ../base\n  - ../remote\n")

	app := kustomizeApp(t, root, "overlay", "    kustomize:\n      components:\n        - ../comp\n")
	targets, err := app.Targets(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tg := range targets {
		names = append(names, tg.Name)
		if len(tg.Notes) != 0 {
			t.Errorf("%s: notes %v without any patch", tg.Name, tg.Notes)
		}
	}
	if want := []string{"web/base-chart", "web/comp-chart"}; !reflect.DeepEqual(names, want) {
		t.Errorf("targets %v, want the base chart and the Application's component", names)
	}

	write(t, filepath.Join(root, "overlay", "kustomization.yaml"), "resources:\n  - ../base\npatches:\n  - path: patch.yaml\n")
	targets, err = kustomizeApp(t, root, "overlay", "").Targets(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || len(targets[0].Notes) != 1 || !strings.Contains(targets[0].Notes[0], "patches") {
		t.Errorf("patched overlay: %+v, want one target with the patches note", targets)
	}
}

func TestChartDirectoryWithKustomizationStaysHelm(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: chart\nversion: 0.1.0\n")
	write(t, filepath.Join(root, "chart", "kustomization.yaml"), "helmCharts:\n  - name: other\n    repo: https://charts.example.com\n")
	targets, err := kustomizeApp(t, root, "chart", "").Targets(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Chart.Chart != filepath.Join(root, "chart") || len(targets[0].Layers) != 0 {
		t.Errorf("targets %+v, want the directory itself as a Helm chart", targets)
	}
}

func TestKustomizeChartsSharingAReleaseNameGetDistinctNames(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "base", "kustomization.yaml"), "helmCharts:\n  - name: extra\n    repo: https://charts.example.com\n")
	write(t, filepath.Join(root, "app", "kustomization.yaml"), `resources:
  - ../base
helmCharts:
  - name: nextcloud
    repo: https://nextcloud.github.io/helm/
    releaseName: nextcloud
  - name: app-template
    repo: https://bjw-s-labs.github.io/helm-charts/
    releaseName: nextcloud
  - name: app-template
    repo: https://bjw-s-labs.github.io/helm-charts/
    releaseName: nextcloud
`)
	targets, err := kustomizeApp(t, root, "app", "").Targets(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tg := range targets {
		names = append(names, tg.Name)
		if len(tg.Files) != 2 {
			t.Errorf("%s: files %v, want the kustomization and its base", tg.Name, tg.Files)
		}
	}
	want := []string{"web/extra", "web/nextcloud/nextcloud", "web/nextcloud/app-template", "web/nextcloud/app-template#2"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names %v, want %v", names, want)
	}
}
