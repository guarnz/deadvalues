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
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
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
