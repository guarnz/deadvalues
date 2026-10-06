package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"helm.sh/helm/v4/pkg/chart/v2/loader"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	repo "helm.sh/helm/v4/pkg/repo/v1"
)

func fluxFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyDir(t, "../../testdata/flux", root)
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	return root
}

type jsonOrigin struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type fluxCheck struct {
	App   string   `json:"app"`
	Notes []string `json:"notes"`
	Keys  []struct {
		Key     string       `json:"key"`
		Status  string       `json:"status"`
		Origins []jsonOrigin `json:"origins"`
	} `json:"keys"`
	Error string `json:"error"`
}

func (c fluxCheck) key(k string) (string, []jsonOrigin) {
	for _, x := range c.Keys {
		if x.Key == k {
			return x.Status, x.Origins
		}
	}
	return "", nil
}

func TestFluxScanBuildsOverlays(t *testing.T) {
	root := fluxFixture(t)
	out, code := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json")
	if code != 1 {
		t.Fatalf("exit %d, want 1 (service.prot is dead)\n%s", code, out)
	}
	var checks []fluxCheck
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	byApp := map[string]fluxCheck{}
	for _, c := range checks {
		if c.Error != "" {
			t.Errorf("%s: %s", c.App, c.Error)
		}
		byApp[c.App] = c
	}
	prod, ok := byApp["production:demo/demo"]
	if !ok {
		t.Fatalf("apps = %v", byApp)
	}
	if _, ok := byApp["staging:demo/demo"]; !ok {
		t.Errorf("staging overlay not scanned: %v", byApp)
	}

	want := map[string]string{
		"image.tag":        "ALIVE",
		"image.repository": "REDUNDANT",
		"replicas":         "ALIVE",
		"labels.env":       "ALIVE",
		"service.prot":     "DEAD",
		"ingress.host":     "CONDITIONAL",
		"name":             "ERROR",
	}
	for k, s := range want {
		if got, _ := prod.key(k); got != s {
			t.Errorf("production %s = %q, want %s", k, got, s)
		}
	}
	if _, o := prod.key("image.tag"); len(o) == 0 || !strings.HasSuffix(o[len(o)-1].File, "apps/production/demo-patch.yaml") || o[len(o)-1].Line != 9 {
		t.Errorf("image.tag origins = %+v, want the production patch", o)
	}
	if _, o := prod.key("replicas"); len(o) != 1 || !strings.HasSuffix(o[0].File, "apps/base/demo/demo-values.yaml") {
		t.Errorf("replicas origins = %+v, want the configMapGenerator file", o)
	}
}

func TestFluxCheckApp(t *testing.T) {
	root := fluxFixture(t)
	file := filepath.Join(root, "infrastructure", "standalone.yaml")
	out, code := runCLI(t, "check", "--app", file, "--repo-root", root, "-o", "json")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var c fluxCheck
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if c.App != "infra/standalone" {
		t.Errorf("app = %q", c.App)
	}
	if s, _ := c.key("service.prot"); s != "DEAD" {
		t.Errorf("service.prot = %q", s)
	}
	if s, o := c.key("image.tag"); s != "ALIVE" || len(o) != 1 || o[0].File != "ConfigMap infra/standalone-tag" {
		t.Errorf("image.tag = %q %+v", s, o)
	}
	if len(c.Notes) == 0 || !strings.Contains(strings.Join(c.Notes, " "), "SOPS") {
		t.Errorf("notes = %v", c.Notes)
	}
}

func TestFluxPruneEditsHelmRelease(t *testing.T) {
	root := fluxFixture(t)
	file := filepath.Join(root, "infrastructure", "standalone.yaml")
	before, _ := os.ReadFile(file)
	if out, code := runCLI(t, "prune", "--app", file, "--repo-root", root, "--only", "dead", "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	after, _ := os.ReadFile(file)
	if strings.Contains(string(after), "prot: 9090") || strings.Contains(string(after), "service:") {
		t.Errorf("service.prot should be pruned:\n%s", after)
	}
	want := strings.Replace(string(before), "    service:\n      prot: 9090\n", "", 1)
	if string(after) != want {
		t.Errorf("only the service block should change:\n%s", after)
	}
}

// helmRepo serves the synthetic chart in versions 0.1.0 and 0.2.0 as an HTTP
// Helm repository.
func helmRepo(t *testing.T) *httptest.Server {
	t.Helper()
	charts := t.TempDir()
	for _, dir := range []string{casesChart, "../../testdata/charts/cases-v2"} {
		c, err := loader.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := chartutil.Save(c, charts); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(charts)))
	t.Cleanup(srv.Close)
	idx, err := repo.IndexDirectory(charts, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.WriteFile(filepath.Join(charts, "index.yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestDiffDirAgainstGitBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv := helmRepo(t)

	root := t.TempDir()
	release := func(version string) string {
		return `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: charts
  namespace: web
spec:
  url: ` + srv.URL + `
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: cases
  namespace: web
spec:
  chart:
    spec:
      chart: cases
      version: ` + version + `
      sourceRef:
        kind: HelmRepository
        name: charts
  values:
    name: app
    labels:
      team: core
    service:
      type: ClusterIP
`
	}
	writeFile(t, filepath.Join(root, "apps", "cases.yaml"), release("0.1.0"))
	writeFile(t, filepath.Join(root, "apps", "unchanged.yaml"), strings.ReplaceAll(release("0.1.0"), "name: cases\n  namespace", "name: other\n  namespace"))
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "base")
	writeFile(t, filepath.Join(root, "apps", "cases.yaml"), release("0.2.0"))

	out, code := runCLI(t, "diff", filepath.Join(root, "apps"), "--base", "HEAD", "--repo-root", root, "-o", "json")
	if code != 1 {
		t.Fatalf("exit %d, want 1 (breaking)\n%s", code, out)
	}
	var d struct {
		App, From, To string
		Changes       []struct{ Key, Kind string }
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("expected a single diff (unchanged release skipped): %v\n%s", err, out)
	}
	if d.App != "web/cases" || d.From != "0.1.0" || d.To != "0.2.0" {
		t.Errorf("diff = %s %s → %s", d.App, d.From, d.To)
	}
	kinds := map[string]string{}
	for _, c := range d.Changes {
		kinds[c.Key] = c.Kind
	}
	if kinds["labels.team"] != "BREAKING" || kinds["service.type"] != "PINNED" || kinds["revisionHistoryLimit"] != "CHANGED" {
		t.Errorf("changes = %v", kinds)
	}

	git("add", "-A")
	git("commit", "-qm", "bump")
	if out, code := runCLI(t, "diff", filepath.Join(root, "apps"), "--base", "HEAD", "--repo-root", root); code != 0 || !strings.Contains(out, "no chart version changed") {
		t.Errorf("nothing changed: exit %d\n%s", code, out)
	}

	// The checkout now goes back to 0.1.0 while --base has 0.2.0: the
	// arguments are swapped, and the diff must say so.
	writeFile(t, filepath.Join(root, "apps", "cases.yaml"), release("0.1.0"))
	stderr := diffStderr(t, filepath.Join(root, "apps"), "--base", "HEAD", "--repo-root", root)
	if !strings.Contains(stderr, "chart versions go down in 1 of 1 targets (e.g. web/cases 0.2.0 → 0.1.0)") {
		t.Errorf("a downgrade-only diff should warn about the direction, stderr:\n%s", stderr)
	}
	git("checkout", "--", ".")
	if stderr := diffStderr(t, filepath.Join(root, "apps"), "--base", "HEAD~1", "--repo-root", root); strings.Contains(stderr, "go down") {
		t.Errorf("an upgrade must not warn, stderr:\n%s", stderr)
	}
}

// TestDiffHeadWithoutCheckout reviews a Renovate-style branch while staying on
// main: --head alone compares the branch with the commit it branched from.
func TestDiffHeadWithoutCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv := helmRepo(t)
	root := t.TempDir()
	manifest := func(version string) string {
		return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: cases
spec:
  sources:
    - repoURL: ` + srv.URL + `
      chart: cases
      targetRevision: ` + version + `
      helm:
        valuesObject:
          name: app
          labels:
            team: core
`
	}
	app := filepath.Join(root, "apps", "cases.yaml")
	writeFile(t, app, manifest("0.1.0"))
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-qm", "base")
	git("switch", "-q", "-c", "renovate/cases")
	writeFile(t, app, manifest("0.2.0"))
	git("commit", "-qam", "bump")
	git("switch", "-q", "main")

	type diffOut struct {
		App, AppFile, From, To string
		Changes                []struct{ Key, Kind string }
	}
	check := func(name string, args ...string) {
		t.Helper()
		out, code := runCLI(t, append([]string{"diff"}, append(args, "--repo-root", root, "-o", "json")...)...)
		var d diffOut
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		if code != 1 || d.From != "0.1.0" || d.To != "0.2.0" || len(d.Changes) == 0 || d.Changes[0].Key != "labels.team" || d.Changes[0].Kind != "BREAKING" {
			t.Errorf("%s: exit %d, %+v", name, code, d)
		}
		if !strings.HasSuffix(d.AppFile, "apps/cases.yaml") || strings.Contains(d.AppFile, "deadvalues-base") {
			t.Errorf("%s: report must point at the repository file, got %q", name, d.AppFile)
		}
	}
	check("--head only", filepath.Join(root, "apps"), "--head", "renovate/cases")
	check("--base and --head", filepath.Join(root, "apps"), "--base", "main", "--head", "renovate/cases")
	check("--app with --head", "--app", app, "--head", "renovate/cases")

	// A branch that only exists on the remote is found without "origin/".
	git("remote", "add", "origin", root)
	git("fetch", "-q", "origin")
	git("branch", "-q", "-D", "renovate/cases")
	check("remote-only branch", filepath.Join(root, "apps"), "--head", "renovate/cases")
	check("explicit origin/", filepath.Join(root, "apps"), "--head", "origin/renovate/cases")
	if _, code := runCLI(t, "diff", filepath.Join(root, "apps"), "--head", "nope", "--repo-root", root); code != 2 {
		t.Errorf("unknown ref: exit %d, want 2", code)
	}
}

// TestDiffValuesFileWithHead starts from the values file: -f alone finds the
// Application that loads it and compares that manifest across the refs.
func TestDiffValuesFileWithHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv := helmRepo(t)
	root := t.TempDir()
	manifest := func(version string) string {
		return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: cases
spec:
  sources:
    - repoURL: https://example.com/repo
      ref: values
    - repoURL: ` + srv.URL + `
      chart: cases
      targetRevision: ` + version + `
      helm:
        valueFiles:
          - $values/config/cases/values.yaml
`
	}
	app := filepath.Join(root, "apps", "cases.yaml")
	vals := filepath.Join(root, "config", "cases", "values.yaml")
	writeFile(t, app, manifest("0.1.0"))
	writeFile(t, vals, "name: app\nlabels:\n  team: core\n")
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	writeFile(t, filepath.Join(root, "charts", "cases", "ci", "test-values.yaml"), "name: app\n")
	writeFile(t, filepath.Join(root, "orphan.yaml"), "a: 1\n")
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-qm", "base")
	git("switch", "-q", "-c", "renovate/cases")
	writeFile(t, app, manifest("0.2.0"))
	git("commit", "-qam", "bump")
	git("switch", "-q", "main")

	if out, code := runCLI(t, "diff", filepath.Join(root, "config", "cases"), "--head", "renovate/cases", "--repo-root", root); code != 0 || !strings.Contains(out, "no chart version changed") {
		t.Errorf("a values directory has no manifest to compare: exit %d\n%s", code, out)
	}

	out, code := runCLI(t, "diff", "-f", vals, "--head", "renovate/cases", "--repo-root", root, "-o", "json")
	var d struct {
		AppFile, From, To string
		Changes           []struct{ Key, Kind string }
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if code != 1 || d.From != "0.1.0" || d.To != "0.2.0" || len(d.Changes) == 0 || d.Changes[0].Key != "labels.team" || d.Changes[0].Kind != "BREAKING" {
		t.Errorf("exit %d, %+v", code, d)
	}
	if !strings.HasSuffix(filepath.ToSlash(d.AppFile), "apps/cases.yaml") {
		t.Errorf("report must point at the Application, got %q", d.AppFile)
	}

	if _, code := runCLI(t, "diff", "-f", filepath.Join(root, "orphan.yaml"), "--head", "renovate/cases", "--repo-root", root); code != 2 {
		t.Errorf("values file nobody loads: exit %d, want 2", code)
	}
	if _, code := runCLI(t, "diff", "-f", filepath.Join(root, "charts", "cases", "ci", "test-values.yaml"), "--head", "renovate/cases", "--repo-root", root); code != 2 {
		t.Errorf("values of a chart directory have no version in git: exit %d, want 2", code)
	}
}

func diffStderr(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, cfg, "{}\n")
	runIO(BuildInfo{Version: "test"}, append([]string{"diff"}, append(args, "--config", cfg, "--cache-dir", t.TempDir())...), &stdout, &stderr)
	return stderr.String()
}
