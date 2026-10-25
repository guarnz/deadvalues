package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The outputs and commands the GitHub Action and other CI setups rely on.

func TestCheckGithubAnnotations(t *testing.T) {
	out, code := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "-o", "github", "--fail-on", "none")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	values := filepath.ToSlash(casesValues)
	for _, want := range []string{
		"::warning file=" + values + ",line=8,title=deadvalues%3A DEAD::",
		"::notice file=" + values + ",line=4,title=deadvalues%3A REDUNDANT::",
		"::warning file=" + values + ",line=18,title=deadvalues%3A CONDITIONAL::",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDiffMarkdownAndGithub(t *testing.T) {
	srv := helmRepo(t)
	args := []string{"diff", "--repo", srv.URL, "--chart", "cases", "--from", "0.1.0", "--to", "0.2.0", "-f", casesValues}

	out, code := runCLI(t, append(args, "-o", "markdown")...)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (breaking)\n%s", code, out)
	}
	for _, want := range []string{
		"— cases 0.1.0 → 0.2.0",
		"| Change | Key | Detail | Source |",
		"| **BREAKING** | `labels.team` | ALIVE → DEAD |",
		"| PINNED | `service.type` |",
		"| CHANGED | `revisionHistoryLimit` |",
		"**1 breaking, 1 pinned, 1 changed, 1 fixed**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown is missing %q:\n%s", want, out)
		}
	}

	out, _ = runCLI(t, append(args, "-o", "github")...)
	for _, want := range []string{
		"::error file=" + filepath.ToSlash(casesValues) + ",line=10,title=deadvalues%3A BREAKING::",
		"title=deadvalues%3A PINNED::",
		"::notice ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("annotations are missing %q:\n%s", want, out)
		}
	}
}

func TestScanChangedSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	data, err := os.ReadFile(casesValues)
	if err != nil {
		t.Fatal(err)
	}
	app := func(name string) string {
		return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ` + name + `
spec:
  sources:
    - repoURL: https://example.com/repo
      ref: values
    - repoURL: https://example.com/repo
      path: charts/cases
      helm:
        valueFiles:
          - $values/values/` + name + `.yaml
`
	}
	for _, name := range []string{"one", "two"} {
		writeFile(t, filepath.Join(root, "apps", name+".yaml"), app(name))
		writeFile(t, filepath.Join(root, "values", name+".yaml"), string(data))
	}
	git := gitIn(t, root)
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "base")

	scanned := func() []string {
		t.Helper()
		out, _ := runCLI(t, "scan", filepath.Join(root, "apps"), "--changed-since", "HEAD", "--repo-root", root, "-o", "json")
		var cs []struct{ App string }
		if err := json.Unmarshal([]byte(out), &cs); err != nil {
			t.Fatalf("decode: %v\n%s", err, out)
		}
		var apps []string
		for _, c := range cs {
			apps = append(apps, c.App)
		}
		return apps
	}

	if apps := scanned(); len(apps) != 0 {
		t.Errorf("nothing changed, scanned %v", apps)
	}
	writeFile(t, filepath.Join(root, "values", "one.yaml"), string(data)+"extra: true\n")
	if apps := scanned(); len(apps) != 1 || apps[0] != "one" {
		t.Errorf("only one's values changed, scanned %v", apps)
	}
	writeFile(t, filepath.Join(root, "charts", "cases", "values.yaml"), "replicas: 1\n")
	if apps := scanned(); len(apps) != 2 {
		t.Errorf("the shared local chart changed, scanned %v", apps)
	}

	if out, code := runCLI(t, "scan", filepath.Join(root, "apps"), "--changed-since", "no-such-ref", "--repo-root", root); code != 2 {
		t.Errorf("an unknown ref is an error: exit %d\n%s", code, out)
	}
}

func TestScanChangedSinceJudgesSharedFiles(t *testing.T) {
	root := sharedRepo(t)
	git := gitIn(t, root)
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "base")

	two := filepath.Join(root, "apps", "two.yaml")
	data, err := os.ReadFile(two)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, two, strings.Replace(string(data), "name: two", "name: two\n  labels:\n    touched: \"true\"", 1))

	out, _ := runCLI(t, "scan", filepath.Join(root, "apps"), "--changed-since", "HEAD", "--repo-root", root, "-o", "json")
	checks := decodeChecks(t, out)
	if len(checks) != 1 || checks[0].App != "two" {
		t.Fatalf("only two changed and only two must be reported, got %+v\n%s", checks, out)
	}
	found := map[string]string{}
	for _, k := range checks[0].Keys {
		found[k.Key] = k.Status
		if k.Key == "labels.team" && k.UsedBy != "one" {
			t.Errorf("labels.team is used by one, which loads the same file: usedBy=%q status=%s", k.UsedBy, k.Status)
		}
	}
	if found["service.prot"] != "DEAD" {
		t.Errorf("service.prot is dead in every app loading the file, got %q\n%s", found["service.prot"], out)
	}
}

func TestScanChangedSinceUsesMergeBase(t *testing.T) {
	root := sharedRepo(t)
	git := gitIn(t, root)
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-qm", "base")
	git("checkout", "-q", "-b", "feature")
	git("checkout", "-q", "main")
	two := filepath.Join(root, "apps", "two.yaml")
	data, err := os.ReadFile(two)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, two, strings.Replace(string(data), "name: two", "name: two\n  labels:\n    touched: \"true\"", 1))
	git("commit", "-qam", "main moves on")
	git("checkout", "-q", "feature")
	writeFile(t, filepath.Join(root, "values", "one-only.yaml"), "replicas: 4\n")

	out, _ := runCLI(t, "scan", filepath.Join(root, "apps"), "--changed-since", "main", "--repo-root", root, "-o", "json")
	checks := decodeChecks(t, out)
	if len(checks) != 1 || checks[0].App != "one" {
		t.Fatalf("only one changed on the branch; two changed on main after it started, got %+v\n%s", checks, out)
	}
}

func TestChangesReportOnlyTheFailuresTheyTouch(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	writeFile(t, filepath.Join(root, "deploy", "apps", "good.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: good
spec:
  sources:
    - repoURL: https://example.com/repo
      ref: values
    - repoURL: https://example.com/repo
      path: charts/cases
      helm:
        valueFiles:
          - $values/values/good.yaml
`)
	data, err := os.ReadFile(casesValues)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "values", "good.yaml"), string(data))
	broken := func(name string) {
		writeFile(t, filepath.Join(root, "deploy", name, "kustomization.yaml"), "resources:\n  - helmrelease.yaml\n")
		writeFile(t, filepath.Join(root, "deploy", name, "helmrelease.yaml"), `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: `+name+`
  namespace: apps
spec:
  chart:
    spec:
      chart: web
      version: 1.0.0
      sourceRef:
        kind: HelmRepository
        name: missing
`)
	}
	broken("old-broken")
	git := gitIn(t, root)
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "base")
	writeFile(t, filepath.Join(root, "values", "good.yaml"), string(data)+"extra: true\n")

	failures := func(out string) []string {
		t.Helper()
		var apps []string
		for _, c := range decodeChecks(t, out) {
			if c.Error != "" {
				apps = append(apps, c.App)
			}
		}
		return apps
	}
	scan := func() []string {
		out, _ := runCLI(t, "scan", filepath.Join(root, "deploy"), "--changed-since", "HEAD", "--repo-root", root, "-o", "json")
		return failures(out)
	}
	diff := func() []string {
		out, _ := runCLI(t, "diff", filepath.Join(root, "deploy"), "--base", "HEAD", "--repo-root", root, "-o", "json")
		return failures(out)
	}

	if got := scan(); len(got) != 0 {
		t.Errorf("scan: the change only touches good, but reported the failures of %v", got)
	}
	if got := diff(); len(got) != 0 {
		t.Errorf("diff: old-broken failed before the change too, but was reported: %v", got)
	}

	broken("new-broken")
	git("add", "-A")
	if got := scan(); len(got) != 1 || !strings.Contains(got[0], "new-broken") {
		t.Errorf("scan: only the new broken app is touched by the change, got %v", got)
	}
	if got := diff(); len(got) != 1 || !strings.Contains(got[0], "new-broken") {
		t.Errorf("diff: only the new broken app fails because of the change, got %v", got)
	}
}

func TestChangesReportOnlyTheAppsNotAnalyzedTheyTouch(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	data, err := os.ReadFile(casesValues)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "values", "good.yaml"), string(data))
	writeFile(t, filepath.Join(root, "deploy", "good.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: good
spec:
  sources:
    - repoURL: https://github.com/acme/home-ops
      ref: values
    - repoURL: https://github.com/acme/home-ops
      path: charts/cases
      helm:
        valueFiles:
          - $values/values/good.yaml
`)
	writeFile(t, filepath.Join(root, "deploy", "vault.yaml"), `apiVersion: argoproj.io/v1alpha1
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
	writeFile(t, filepath.Join(root, "deploy", "sure.yaml"), `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: sure
  namespace: sure
spec:
  url: https://github.com/acme/sure
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: sure
  namespace: sure
spec:
  chart:
    spec:
      chart: ./charts/sure
      sourceRef:
        kind: GitRepository
        name: sure
  values:
    replicas: 1
`)
	git := gitIn(t, root)
	git("init", "-q")
	git("remote", "add", "origin", "https://github.com/acme/home-ops.git")
	git("add", "-A")
	git("commit", "-qm", "base")
	t.Chdir(root)

	reported := func() []string {
		t.Helper()
		out, _ := runCLI(t, "scan", filepath.Join(root, "deploy"), "--changed-since", "HEAD", "--repo-root", root, "-o", "json", "--fail-on", "none")
		var apps []string
		for _, c := range decodeChecks(t, out) {
			apps = append(apps, c.App)
		}
		return apps
	}

	writeFile(t, filepath.Join(root, "values", "good.yaml"), string(data)+"extra: true\n")
	if got := reported(); len(got) != 1 || got[0] != "good" {
		t.Errorf("only good changed, but the report has %v", got)
	}
	git("checkout", "-q", "--", ".")
	writeFile(t, filepath.Join(root, "deploy", "sure.yaml"), strings.Replace(mustRead(t, filepath.Join(root, "deploy", "sure.yaml")), "replicas: 1", "replicas: 2", 1))
	if got := reported(); len(got) != 1 || got[0] != "sure/sure" {
		t.Errorf("only sure changed, but the report has %v", got)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFailedTargetIsReported(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	writeFile(t, filepath.Join(root, "apps", "broken.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: broken
spec:
  sources:
    - repoURL: https://example.com/repo
      ref: values
    - repoURL: https://example.com/repo
      path: charts/cases
      helm:
        valueFiles:
          - $values/values/missing.yaml
`)
	dir := filepath.Join(root, "apps")

	out, code := runCLI(t, "scan", dir, "--repo-root", root, "--details", "-o", "markdown")
	if code != 2 {
		t.Errorf("a target that cannot be analyzed exits 2, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "| `broken` | **Error:** ") || !strings.Contains(out, "> **Error:** ") {
		t.Errorf("markdown must show the error in the summary and the details:\n%s", out)
	}

	out, _ = runCLI(t, "scan", dir, "--repo-root", root, "-o", "github")
	if !strings.Contains(out, "::error ") || !strings.Contains(out, "title=deadvalues%3A broken::") {
		t.Errorf("github output must annotate the failed target:\n%s", out)
	}
}
