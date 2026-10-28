package cli

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestApplicationAndScan(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	data, _ := os.ReadFile(casesValues)
	writeFile(t, filepath.Join(root, "values", "cases.yaml"), string(data))
	writeFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  destination:
    namespace: apps
  sources:
    - repoURL: https://example.com/repo
      targetRevision: HEAD
      ref: values
    - repoURL: https://example.com/repo
      targetRevision: HEAD
      path: charts/cases
      helm:
        valueFiles:
          - $values/values/cases.yaml
        parameters:
          - name: replicas
            value: "5"
`)
	writeFile(t, filepath.Join(root, "apps", "not-an-app.yaml"), "kind: ConfigMap\n")

	out, _ := runCLI(t, "check", "--app", filepath.Join(root, "apps", "demo.yaml"), "--repo-root", root, "-o", "json")
	c, keys := statuses(t, out)
	if c.App != "demo" {
		t.Errorf("app = %q, want demo", c.App)
	}
	rep := keys["replicas"]
	if rep.Status != "ALIVE" || len(rep.Origins) != 2 || rep.Origins[1].File != "demo: helm.parameters" {
		t.Errorf("replicas = %+v, want ALIVE from the values file and helm.parameters", rep)
	}

	out, code := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "--color", "never")
	if code != 1 || !strings.Contains(out, "demo") || !strings.Contains(out, "cases 0.1.0") {
		t.Errorf("scan: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "1 app · 1 with dead keys · 1 dead · 3 conditional · 4 redundant") {
		t.Errorf("scan should end with the totals line:\n%s", out)
	}
}

func TestEveryApplicationInAFile(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	app := func(name, path string) string {
		body := `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ` + name + `
spec:
  destination:
    namespace: apps
  source:
    repoURL: https://example.com/repo
    path: ` + path + `
`
		if strings.HasPrefix(path, "charts/") {
			body += "    helm:\n      values: |\n        name: " + name + "\n"
		}
		return body
	}
	file := filepath.Join(root, "apps", "all.yaml")
	writeFile(t, file, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: apps\n---\n"+
		app("one", "charts/cases")+"---\n"+app("manifests", "manifests/plain")+"---\n"+app("two", "charts/cases"))

	names := func(out string) []string {
		t.Helper()
		var got []string
		for _, c := range decodeChecks(t, out) {
			got = append(got, c.App)
		}
		sort.Strings(got)
		return got
	}
	want := []string{"one", "two"}
	out, code := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json", "--fail-on", "none")
	if code != 0 || !slices.Equal(names(out), want) {
		t.Errorf("scan: exit %d, apps %v, want %v\n%s", code, names(out), want, out)
	}
	out, code = runCLI(t, "check", "--app", file, "--repo-root", root, "-o", "json", "--fail-on", "none")
	if code != 0 || !slices.Equal(names(out), want) {
		t.Errorf("check --app: exit %d, apps %v, want %v\n%s", code, names(out), want, out)
	}
}

func TestChartFromAnotherGitRepoIsNotAnalyzed(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	writeFile(t, filepath.Join(root, "apps", "web.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  source:
    repoURL: https://example.com/repo
    path: charts/cases
    helm:
      values: |
        name: web
`)
	vault := filepath.Join(root, "apps", "vault.yaml")
	writeFile(t, vault, `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: vault
spec:
  source:
    repoURL: https://github.com/hashicorp/vault-helm.git
    targetRevision: v0.25.0
    path: .
    helm:
      values: |
        server:
          ha:
            enabled: true
`)
	byApp := func(out string) map[string]anyCheck {
		t.Helper()
		m := map[string]anyCheck{}
		for _, c := range decodeChecks(t, out) {
			m[c.App] = c
		}
		return m
	}

	out, code := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json", "--fail-on", "none")
	got := byApp(out)
	if code != 0 || got["vault"].Skipped == "" || got["vault"].Error != "" || got["web"].Error != "" {
		t.Errorf("scan: exit %d, want vault reported as not analyzed, not as an error\n%s", code, out)
	}
	if !strings.Contains(got["vault"].Skipped, "vault-helm") {
		t.Errorf("the reason should name the chart repository: %q", got["vault"].Skipped)
	}

	out, code = runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "--fail-on", "none")
	if code != 0 || !strings.Contains(out, "not analyzed:") || !strings.Contains(out, "1 not analyzed") {
		t.Errorf("table: exit %d, want a not analyzed row and count\n%s", code, out)
	}

	out, code = runCLI(t, "check", "--app", vault, "--repo-root", root, "-o", "json")
	if code != 0 || byApp(out)["vault"].Skipped == "" {
		t.Errorf("check --app: exit %d, want vault reported as not analyzed\n%s", code, out)
	}
}

func TestArgoHelmAPIVersions(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	app := func(apis string) string {
		return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  source:
    repoURL: https://example.com/repo
    path: charts/cases
    helm:` + apis + `
      values: |
        name: web
        monitoring:
          serviceMonitor: true
`
	}
	for _, tt := range []struct{ apis, want string }{
		{"", "CONDITIONAL"},
		{"\n      apiVersions:\n        - monitoring.coreos.com/v1/ServiceMonitor", "ALIVE"},
	} {
		file := filepath.Join(root, "apps", "web.yaml")
		writeFile(t, file, app(tt.apis))
		out, code := runCLI(t, "check", "--app", file, "--repo-root", root, "-o", "json", "--fail-on", "none")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		_, keys := statuses(t, out)
		if got := keys["monitoring.serviceMonitor"].Status; !strings.HasPrefix(got, tt.want) {
			t.Errorf("helm block %q: monitoring.serviceMonitor is %q, want %s", tt.apis, got, tt.want)
		}
	}
}
