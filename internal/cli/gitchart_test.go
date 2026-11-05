package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustDecode(t *testing.T, out string, v any) {
	t.Helper()
	if err := decodeReport(out, v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// chartRepository makes a git repository with the synthetic chart and a values file,
// and returns its URL; the consumer next to it has another origin.
func chartRepository(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	dir := t.TempDir()
	copyDir(t, casesChart, filepath.Join(dir, "charts", "cases"))
	writeFile(t, filepath.Join(dir, "charts", "cases", "values-extra.yaml"), "replicaCount: 2\n")
	writeFile(t, filepath.Join(dir, "envs", "web.yaml"), "name: web\nservice:\n  prot: 1\n")
	git := gitIn(t, dir)
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "chart")
	git("tag", "v1")
	return "file://" + filepath.ToSlash(dir)
}

func consumer(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	git := gitIn(t, root)
	git("init", "-q")
	git("remote", "add", "origin", "https://github.com/acme/home-ops.git")
	return root
}

func TestScanArgoChartAndValuesFromAnotherRepository(t *testing.T) {
	url := chartRepository(t)
	app := `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  sources:
    - repoURL: ` + url + `
      ref: values
      targetRevision: v1
    - repoURL: ` + url + `
      path: charts/cases
      targetRevision: main
      helm:
        valueFiles:
          - $values/envs/web.yaml
          - values-extra.yaml
`
	root := consumer(t, map[string]string{"apps/web.yaml": app})
	out, code := runCLI(t, "check", "--app", filepath.Join(root, "apps", "web.yaml"), "--repo-root", root, "-o", "json")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var c fluxCheck
	mustDecode(t, out, &c)
	s, o := c.key("service.prot")
	if s != "DEAD" || len(o) != 1 || o[0].File != url+"/envs/web.yaml@v1" || o[0].Line != 3 {
		t.Errorf("service.prot = %q %+v", s, o)
	}
	if s, _ := c.key("replicaCount"); s == "" {
		t.Errorf("the value file next to the chart was not read:\n%s", out)
	}

	cache := t.TempDir()
	if out, code := runCLI(t, "prune", "--app", filepath.Join(root, "apps", "web.yaml"), "--repo-root", root, "--only", "dead", "--yes", "--cache-dir", cache); code != 0 {
		t.Fatalf("prune: exit %d\n%s", code, out)
	}
	matches, _ := filepath.Glob(filepath.Join(cache, "git", "*", "*", "*", "envs", "web.yaml"))
	for _, f := range matches {
		if data, _ := os.ReadFile(f); !strings.Contains(string(data), "prot") {
			t.Errorf("prune edited the clone: %s", data)
		}
	}
}

func TestScanFluxChartFromAnotherRepository(t *testing.T) {
	url := chartRepository(t)
	root := consumer(t, map[string]string{
		"apps/web.yaml": `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata: {name: charts, namespace: flux-system}
spec:
  url: ` + url + `
  ref: {tag: v1}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: web, namespace: apps}
spec:
  chart:
    spec:
      chart: charts/cases
      sourceRef: {kind: GitRepository, name: charts, namespace: flux-system}
  values:
    name: web
    service:
      prot: 1
`,
	})
	out, code := runCLI(t, "check", "--app", filepath.Join(root, "apps", "web.yaml"), "--repo-root", root, "-o", "json")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var c fluxCheck
	mustDecode(t, out, &c)
	if s, _ := c.key("service.prot"); s != "DEAD" {
		t.Errorf("service.prot = %q\n%s", s, out)
	}
}

func TestChartThatCannotBeFetchedIsNotAnalyzed(t *testing.T) {
	url := chartRepository(t)
	root := consumer(t, map[string]string{
		"apps/web.yaml": `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  source:
    repoURL: ` + url + `-private
    path: charts/cases
    helm: {}
`,
	})
	out, code := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json", "--fail-on", "none")
	var got []struct{ Error, Skipped string }
	mustDecode(t, out, &got)
	if code != 0 || len(got) != 1 || got[0].Error != "" || !strings.Contains(got[0].Skipped, "give git access") {
		t.Errorf("exit %d: %+v", code, got)
	}
}

func TestDiffBetweenRevisionsOfAChartFromGit(t *testing.T) {
	url := chartRepository(t)
	app := "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: web\nspec:\n  source:\n    repoURL: " + url + "\n    path: charts/cases\n    targetRevision: %s\n    helm:\n      values: |\n        name: web\n"
	root := consumer(t, map[string]string{"apps/web.yaml": strings.Replace(app, "%s", "v1", 1)})
	git := gitIn(t, root)
	git("add", "-A")
	git("commit", "-q", "-m", "app")
	writeFile(t, filepath.Join(root, "apps", "web.yaml"), strings.Replace(app, "%s", "main", 1))
	git("commit", "-q", "-am", "bump")
	out, code := runCLI(t, "diff", filepath.Join(root, "apps"), "--base", "HEAD~1", "--head", "HEAD", "--repo-root", root, "--fail-on", "none", "-o", "json")
	if code != 0 || strings.Contains(out, "no chart version changed") {
		t.Errorf("exit %d, a new revision must be compared:\n%s", code, out)
	}
}
