package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type anyCheck struct {
	App   string `json:"app"`
	Chart string `json:"chart"`
	Keys  []struct {
		Key    string `json:"key"`
		Status string `json:"status"`
		UsedBy string `json:"usedBy"`
	} `json:"keys"`
	Error string `json:"error"`
}

func decodeChecks(t *testing.T, out string) []anyCheck {
	t.Helper()
	var many []anyCheck
	if err := json.Unmarshal([]byte(out), &many); err == nil {
		return many
	}
	var one anyCheck
	if err := json.Unmarshal([]byte(out), &one); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return []anyCheck{one}
}

// sharedRepo: two Applications load values/shared.yaml; "one" renders the
// cases chart (reads labels) and also loads values/one-only.yaml, "two"
// renders cases-v2 (renamed labels to podLabels).
func sharedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	copyDir(t, "../../testdata/charts/cases-v2", filepath.Join(root, "charts", "cases-v2"))
	writeFile(t, filepath.Join(root, "values", "shared.yaml"), "name: app\nlabels:\n  team: core\nservice:\n  prot: 8080\n")
	writeFile(t, filepath.Join(root, "values", "one-only.yaml"), "replicas: 3\n")
	app := func(name, chart string, files ...string) string {
		s := "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: " + name + "\nspec:\n  sources:\n" +
			"    - repoURL: https://example.com/repo\n      ref: values\n" +
			"    - repoURL: https://example.com/repo\n      path: charts/" + chart + "\n      helm:\n        valueFiles:\n"
		for _, f := range files {
			s += "          - $values/values/" + f + "\n"
		}
		return s
	}
	writeFile(t, filepath.Join(root, "apps", "one.yaml"), app("one", "cases", "shared.yaml", "one-only.yaml"))
	writeFile(t, filepath.Join(root, "apps", "two.yaml"), app("two", "cases-v2", "shared.yaml"))
	return root
}

func TestCheckValuesFileAlone(t *testing.T) {
	root := sharedRepo(t)
	out, code := runCLI(t, "check", "-f", filepath.Join(root, "values", "shared.yaml"), "--repo-root", root, "-o", "json")
	if code != 1 {
		t.Fatalf("exit %d, want 1 (service.prot is dead everywhere)\n%s", code, out)
	}
	checks := decodeChecks(t, out)
	if len(checks) != 2 {
		t.Fatalf("want both apps that load the file, got %d\n%s", len(checks), out)
	}
	for _, c := range checks {
		for _, k := range c.Keys {
			if k.Key == "replicas" {
				t.Errorf("%s: replicas comes from another values file and must not be reported", c.App)
			}
			if k.Key == "service.prot" && k.Status != "DEAD" {
				t.Errorf("%s: service.prot = %s", c.App, k.Status)
			}
			if c.App == "two" && k.Key == "labels.team" && k.UsedBy != "one" {
				t.Errorf("two: labels.team is dead in cases-v2 but used by one, got usedBy=%q", k.UsedBy)
			}
		}
	}
}

func TestCheckValuesInsideChartDir(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	data, _ := os.ReadFile(casesValues)
	writeFile(t, filepath.Join(root, "charts", "cases", "ci", "test-values.yaml"), string(data))

	out, code := runCLI(t, "check", "-f", filepath.Join(root, "charts", "cases", "ci", "test-values.yaml"), "--repo-root", root, "-o", "json")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	c := decodeChecks(t, out)[0]
	if c.App != "cases" || c.Chart != "cases 0.1.0" {
		t.Errorf("app %q chart %q", c.App, c.Chart)
	}

	writeFile(t, filepath.Join(root, "orphan.yaml"), "a: 1\n")
	if _, code := runCLI(t, "check", "-f", filepath.Join(root, "orphan.yaml"), "--repo-root", root); code != 2 {
		t.Errorf("values file nobody uses: exit %d, want 2", code)
	}
}

func TestCheckValuesFileUsedByFluxOverlays(t *testing.T) {
	root := fluxFixture(t)
	out, _ := runCLI(t, "check", "-f", filepath.Join(root, "apps", "base", "demo", "demo-values.yaml"), "--repo-root", root, "-o", "json")
	checks := decodeChecks(t, out)
	apps := map[string]bool{}
	for _, c := range checks {
		apps[c.App] = true
		for _, k := range c.Keys {
			if k.Key != "replicas" && k.Key != "image.repository" {
				t.Errorf("%s: %s does not come from demo-values.yaml", c.App, k.Key)
			}
		}
	}
	if !apps["apps/production:demo/demo"] || !apps["apps/staging:demo/demo"] {
		t.Errorf("apps = %v", apps)
	}
}

func TestPruneSharedFileNeedsEveryApp(t *testing.T) {
	root := sharedRepo(t)
	shared := filepath.Join(root, "values", "shared.yaml")

	if out, code := runCLI(t, "prune", "--app", filepath.Join(root, "apps", "two.yaml"), "--repo-root", root, "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if data, _ := os.ReadFile(shared); !strings.Contains(string(data), "prot: 8080") || !strings.Contains(string(data), "team: core") {
		t.Fatalf("prune --app must not edit a file other apps load:\n%s", data)
	}

	if out, code := runCLI(t, "prune", "-f", shared, "--repo-root", root, "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	data, _ := os.ReadFile(shared)
	if strings.Contains(string(data), "prot:") {
		t.Errorf("service.prot is dead in every app and should be pruned:\n%s", data)
	}
	if !strings.Contains(string(data), "team: core") {
		t.Errorf("labels.team is used by app one and must stay:\n%s", data)
	}
	if one, _ := os.ReadFile(filepath.Join(root, "values", "one-only.yaml")); string(one) != "replicas: 3\n" {
		t.Errorf("prune -f must only edit the requested file, one-only.yaml = %q", one)
	}
}

func TestSharedFileWithAppsInOtherFolders(t *testing.T) {
	root := sharedRepo(t)
	data, _ := os.ReadFile(filepath.Join(root, "apps", "two.yaml"))
	writeFile(t, filepath.Join(root, "clusters", "prod", "two.yaml"), string(data))
	if err := os.Remove(filepath.Join(root, "apps", "two.yaml")); err != nil {
		t.Fatal(err)
	}

	out, code := runCLI(t, "check", "--app", filepath.Join(root, "clusters", "prod", "two.yaml"), "--repo-root", root, "--color", "never")
	if code != 0 || !strings.Contains(out, "SHARED") || !strings.Contains(out, "shared with one") {
		t.Errorf("an app in another folder must still be found: exit %d\n%s", code, out)
	}
}

// fluxSharedRepo: HelmReleases a (cases) and b (cases-v2) both load
// shared/values.yaml through a configMapGenerator.
func fluxSharedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	copyDir(t, "../../testdata/charts/cases-v2", filepath.Join(root, "charts", "cases-v2"))
	writeFile(t, filepath.Join(root, "shared", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: demo
configMapGenerator:
  - name: shared-values
    files:
      - values.yaml=values.yaml
generatorOptions:
  disableNameSuffixHash: true
`)
	writeFile(t, filepath.Join(root, "shared", "values.yaml"), "name: app\nlabels:\n  team: core\nservice:\n  prot: 8080\n")
	writeFile(t, filepath.Join(root, "releases", "source.yaml"), `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: platform
  namespace: demo
spec:
  url: https://example.com/platform
`)
	release := func(name, chart string) string {
		return `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: ` + name + `
  namespace: demo
spec:
  chart:
    spec:
      chart: ./charts/` + chart + `
      sourceRef:
        kind: GitRepository
        name: platform
  valuesFrom:
    - kind: ConfigMap
      name: shared-values
`
	}
	writeFile(t, filepath.Join(root, "releases", "a.yaml"), release("a", "cases"))
	writeFile(t, filepath.Join(root, "releases", "b.yaml"), release("b", "cases-v2"))
	return root
}

func TestFluxSharedValuesFile(t *testing.T) {
	root := fluxSharedRepo(t)
	shared := filepath.Join(root, "shared", "values.yaml")
	b := filepath.Join(root, "releases", "b.yaml")

	out, code := runCLI(t, "check", "--app", b, "--repo-root", root, "--color", "never")
	if code != 0 || !strings.Contains(out, "SHARED") || !strings.Contains(out, "shared with demo/a") {
		t.Errorf("Flux releases sharing a values file: exit %d\n%s", code, out)
	}

	if out, code := runCLI(t, "prune", "--app", b, "--repo-root", root, "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if data, _ := os.ReadFile(shared); !strings.Contains(string(data), "prot: 8080") || !strings.Contains(string(data), "team: core") {
		t.Fatalf("prune --app on a Flux release must not edit a file another release loads:\n%s", data)
	}

	if out, code := runCLI(t, "prune", "-f", shared, "--repo-root", root, "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	data, _ := os.ReadFile(shared)
	if strings.Contains(string(data), "prot:") || !strings.Contains(string(data), "team: core") {
		t.Errorf("prune -f must drop the key no release uses and keep labels.team (used by a):\n%s", data)
	}
}

func TestReleasesInOneFileDoNotShareValues(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	copyDir(t, "../../testdata/charts/cases-v2", filepath.Join(root, "charts", "cases-v2"))
	release := func(name, chart string) string {
		return `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: ` + name + `
  namespace: demo
spec:
  chart:
    spec:
      chart: ./charts/` + chart + `
      sourceRef:
        kind: GitRepository
        name: platform
  values:
    name: ` + name + `
    labels:
      team: core
`
	}
	writeFile(t, filepath.Join(root, "apps", "releases.yaml"), `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: platform
  namespace: demo
spec:
  url: https://example.com/platform
---
`+release("x", "cases-v2")+"---\n"+release("y", "cases"))

	out, _ := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json")
	for _, c := range decodeChecks(t, out) {
		if c.App != "demo/x" {
			continue
		}
		for _, k := range c.Keys {
			if k.Key == "labels.team" && (k.Status != "DEAD" || k.UsedBy != "") {
				t.Errorf("x's own spec.values labels.team is dead in cases-v2; y's block in the same file must not hide it: %+v", k)
			}
		}
		return
	}
	t.Fatalf("release x not scanned:\n%s", out)
}

func TestChartFromHelmRepoAlias(t *testing.T) {
	srv := helmRepo(t)
	cfg := filepath.Join(t.TempDir(), "repositories.yaml")
	writeFile(t, cfg, "apiVersion: \"\"\nrepositories:\n  - name: testrepo\n    url: "+srv.URL+"\n")
	t.Setenv("HELM_REPOSITORY_CONFIG", cfg)

	out, _ := runCLI(t, "check", "--chart", "testrepo/cases", "-f", casesValues, "-o", "json")
	if c := decodeChecks(t, out)[0]; c.Chart != "cases 0.2.0" {
		t.Errorf("without --version the latest chart is used, got %q", c.Chart)
	}
	out, _ = runCLI(t, "check", "--chart", "testrepo/cases", "--version", "0.1.0", "-f", casesValues, "-o", "json")
	if c := decodeChecks(t, out)[0]; c.Chart != "cases 0.1.0" {
		t.Errorf("--version 0.1.0 got %q", c.Chart)
	}
	if _, code := runCLI(t, "check", "--chart", "nope/cases", "-f", casesValues); code != 2 {
		t.Errorf("unknown repository alias: exit %d, want 2", code)
	}
}
