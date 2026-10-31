package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

func TestScanUsesFluxKustomizationNamespace(t *testing.T) {
	root := fluxFixture(t)
	writeFile(t, filepath.Join(root, "clusters", "main", "apps.yaml"), `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: production
  namespace: flux-system
spec:
  path: ./apps/production
  targetNamespace: shop
`)
	out, _ := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json", "--fail-on", "none")
	var checks []fluxCheck
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var apps []string
	for _, c := range checks {
		apps = append(apps, c.App)
	}
	if got := strings.Join(apps, " "); got != "production:shop/demo staging:demo/demo" {
		t.Errorf("apps = %q, want production in the Kustomization's targetNamespace and staging unchanged", got)
	}
}

func TestCheckAppliesFluxKustomizationVars(t *testing.T) {
	root := fluxFixture(t)
	writeFile(t, filepath.Join(root, "infrastructure", "vars", "release.yaml"), `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: vars
  namespace: infra
spec:
  chart:
    spec:
      chart: ./charts/cases
      sourceRef:
        kind: GitRepository
        name: platform
  values:
    name: ${APP_NAME}
    labels:
      team: ${TEAM}
`)
	writeFile(t, filepath.Join(root, "clusters", "main", "vars.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: team-vars
  namespace: flux-system
data:
  TEAM: payments
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: vars
  namespace: flux-system
spec:
  path: ./infrastructure/vars
  postBuild:
    substitute:
      APP_NAME: web
    substituteFrom:
      - kind: ConfigMap
        name: team-vars
      - kind: Secret
        name: absent
`)
	out, _ := runCLI(t, "check", "--app", filepath.Join(root, "infrastructure", "vars", "release.yaml"), "--repo-root", root, "-o", "json", "--fail-on", "none")
	var c struct {
		Notes []string `json:"notes"`
		Keys  []struct {
			Key   string      `json:"key"`
			Value interface{} `json:"value"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := map[string]interface{}{}
	for _, k := range c.Keys {
		got[k.Key] = k.Value
	}
	if got["name"] != "web" || got["labels.team"] != "payments" {
		t.Errorf("values = %v, want name from substitute and labels.team from substituteFrom", got)
	}
	if !strings.Contains(strings.Join(c.Notes, "\n"), "variables from Secret absent are not in the repository") {
		t.Errorf("notes = %q", c.Notes)
	}
}

func TestScanSkipByAnyNameOrDirectory(t *testing.T) {
	root := fluxFixture(t)
	broken := filepath.Join(root, "apps", "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"kustomization.yaml": "resources:\n  - ../base/demo\npatches:\n  - path: missing.yaml\n",
		"missing.yaml":       "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: does-not-exist\ndata:\n  a: b\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(broken, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	scan := func(skip ...string) []string {
		args := []string{"scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json", "--fail-on", "none"}
		for _, s := range skip {
			args = append(args, "--skip", s)
		}
		out, _ := runCLI(t, args...)
		var checks []fluxCheck
		if strings.TrimSpace(out) != "" {
			if err := json.Unmarshal([]byte(out), &checks); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		}
		var apps []string
		for _, c := range checks {
			apps = append(apps, c.App)
		}
		return apps
	}

	all := scan()
	if !strings.Contains(strings.Join(all, " "), "apps/broken") {
		t.Fatalf("a failed kustomize build should be reported by its directory: %v", all)
	}
	for _, tt := range []struct {
		skip []string
		want string
	}{
		{[]string{"demo"}, "apps/broken"},
		{[]string{"demo/demo", broken}, ""},
		{[]string{"production:demo/demo", broken}, "staging:demo/demo"},
		{[]string{filepath.Join(root, "apps", "staging"), broken}, "production:demo/demo"},
	} {
		if got := strings.Join(scan(tt.skip...), " "); got != tt.want {
			t.Errorf("--skip %v: apps = %q, want %q", tt.skip, got, tt.want)
		}
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
	git := gitIn(t, root)
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
	for _, skip := range []string{"cases", "web/cases", filepath.Join(root, "apps")} {
		if out, code := runCLI(t, "diff", filepath.Join(root, "apps"), "--base", "HEAD", "--repo-root", root, "--skip", skip); code != 0 || !strings.Contains(out, "no chart version changed") {
			t.Errorf("--skip %s: exit %d\n%s", skip, code, out)
		}
	}
	if _, code := runCLI(t, "diff", "--app", filepath.Join(root, "apps", "cases.yaml"), "--to", "0.2.0", "--skip", "cases"); code != 2 {
		t.Errorf("--skip with --app: exit %d, want 2", code)
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
	git := gitIn(t, root)
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
	git := gitIn(t, root)
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

func runCLISplit(t *testing.T, args ...string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, cfg, "{}\n")
	runIO(BuildInfo{Version: "test"}, append(args, "--config", cfg, "--cache-dir", t.TempDir()), &stdout, &stderr)
	return stdout.String(), stderr.String()
}

func diffStderr(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, cfg, "{}\n")
	runIO(BuildInfo{Version: "test"}, append([]string{"diff"}, append(args, "--config", cfg, "--cache-dir", t.TempDir())...), &stdout, &stderr)
	return stderr.String()
}

func TestFluxChartFromAnotherGitRepoIsNotAnalyzed(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	writeFile(t, filepath.Join(root, "apps", "sources.yaml"), `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: external
  namespace: flux-system
spec:
  url: https://github.com/acme/charts.git
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: flux-system
  namespace: flux-system
spec:
  url: ssh://git@github.com/acme/home-ops
`)
	release := func(name, chart, source string) {
		writeFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: `+name+`
  namespace: apps
spec:
  chart:
    spec:
      chart: `+chart+`
      sourceRef:
        kind: GitRepository
        name: `+source+`
        namespace: flux-system
  values:
    name: web
`)
	}
	release("external", "./charts/web", "external")
	release("local", "./charts/cases", "flux-system")
	release("missing", "./charts/nope", "flux-system")
	git := gitIn(t, root)
	git("init", "-q")
	git("remote", "add", "origin", "https://github.com/acme/home-ops.git")

	out, _ := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json", "--fail-on", "none")
	got := map[string]anyCheck{}
	for _, c := range decodeChecks(t, out) {
		got[c.App] = c
	}
	if c := got["apps/external"]; c.Error != "" || !strings.Contains(c.Skipped, "github.com/acme/charts") {
		t.Errorf("external: want not analyzed with the repository, got skipped %q, error %q", c.Skipped, c.Error)
	}
	if c := got["apps/local"]; c.Error != "" || c.Skipped != "" {
		t.Errorf("local: the GitRepository is this checkout, want it analyzed, got skipped %q, error %q", c.Skipped, c.Error)
	}
	if c := got["apps/missing"]; !strings.Contains(c.Error, "not in this repository") {
		t.Errorf("missing: a wrong path in this repository is still an error, got skipped %q, error %q", c.Skipped, c.Error)
	}
}

func TestScanSkipsKustomizeComponents(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components", "anubis", "kustomization.yaml"),
		"apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - helmrelease.yaml\n")
	writeFile(t, filepath.Join(root, "components", "anubis", "helmrelease.yaml"), `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: ${APP}-anubis
spec:
  chart:
    spec:
      chart: app-template
      sourceRef:
        kind: HelmRepository
        name: bjw-s
`)
	out, code := runCLI(t, "scan", root, "--repo-root", root, "-o", "json", "--fail-on", "none")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if apps := decodeChecks(t, out); len(apps) != 0 {
		t.Errorf("a component is only built through what includes it, got apps %+v", apps)
	}
}

func TestScanFollowsTheFluxGraph(t *testing.T) {
	root := fluxFixture(t)
	cluster := func(name, apps string) {
		dir := filepath.Join(root, "clusters", name)
		writeFile(t, filepath.Join(dir, "flux-system", "kustomization.yaml"), "resources: [gotk-sync.yaml]\n")
		writeFile(t, filepath.Join(dir, "flux-system", "gotk-sync.yaml"), `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: flux-system
  namespace: flux-system
spec:
  path: ./clusters/`+name+`
`)
		writeFile(t, filepath.Join(dir, "apps.yaml"), `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps
  namespace: flux-system
spec:
  path: ./apps/`+apps+`
  targetNamespace: shop-`+name+`
`)
	}
	cluster("eu", "production")
	cluster("us", "production")

	out, _ := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json", "--fail-on", "none")
	got := map[string]anyCheck{}
	for _, c := range decodeChecks(t, out) {
		got[c.App] = c
	}
	for _, app := range []string{"eu:production:shop-eu/demo", "us:production:shop-us/demo"} {
		if c, ok := got[app]; !ok || c.Error != "" || c.Skipped != "" {
			t.Errorf("%s: want it analyzed once per cluster, got %+v in %v", app, c, got)
		}
	}
	if c := got["staging:demo"]; !strings.Contains(c.Skipped, "not applied") || !strings.Contains(c.Skipped, "apps/staging") {
		t.Errorf("no Kustomization applies the staging overlay, got skipped %q in %v", c.Skipped, got)
	}
}

func TestScanFindsCRDsTheClusterInstalls(t *testing.T) {
	root := t.TempDir()
	chart := func(name, templates string, files map[string]string) {
		dir := filepath.Join(root, "charts", name)
		writeFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: "+name+"\nversion: 0.1.0\n")
		writeFile(t, filepath.Join(dir, "templates", "all.yaml"), templates)
		for f, c := range files {
			writeFile(t, filepath.Join(dir, f), c)
		}
	}
	crd := func(group string) string {
		return "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: widgets." + group + "\nspec:\n  group: " + group + "\n  names: {kind: Widget, plural: widgets}\n  scope: Namespaced\n  versions:\n    - {name: v1, served: true, storage: true}\n"
	}
	chart("app", `{{- if .Capabilities.APIVersions.Has "static.example.com/v1" }}
kind: Widget
metadata: {name: {{ .Values.fromChartDir }}}
{{- end }}
---
{{- if .Capabilities.APIVersions.Has "templated.example.com/v1" }}
kind: Widget
metadata: {name: {{ .Values.fromTemplate }}}
{{- end }}
---
{{- if .Capabilities.APIVersions.Has "plain.example.com/v1/Widget" }}
kind: Widget
metadata: {name: {{ .Values.fromPlainFile }}}
{{- end }}
---
{{- if .Capabilities.APIVersions.Has "off.example.com/v1" }}
kind: Widget
metadata: {name: {{ .Values.fromDisabled }}}
{{- end }}
`, nil)
	chart("guarded", `{{- if not (or (.Capabilities.APIVersions.Has "static.example.com/v1") .Values.trustCRDs) }}
{{- fail "needs the CRD" }}
{{- end }}
kind: ConfigMap
metadata: {name: guarded}
`, nil)
	chart("operator", "kind: ConfigMap\nmetadata: {name: x}\n", map[string]string{"crds/widget.yaml": crd("static.example.com")})
	chart("templated", "{{- if .Values.crds.enabled }}\n"+crd("templated.example.com")+"{{- end }}\n", map[string]string{"values.yaml": "crds: {enabled: false}\n"})
	chart("disabled", "{{- if .Values.crds.enabled }}\n"+crd("off.example.com")+"{{- end }}\n", map[string]string{"values.yaml": "crds: {enabled: false}\n"})

	release := func(name, chartName, values string) string {
		return `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: ` + name + `
  namespace: apps
spec:
  chart:
    spec:
      chart: ./charts/` + chartName + `
      sourceRef:
        kind: GitRepository
        name: flux-system
        namespace: flux-system
  values:
` + values
	}
	app := "    fromChartDir: a\n    fromTemplate: b\n    fromPlainFile: c\n    fromDisabled: d\n"
	cluster := func(name string) {
		dir := filepath.Join(root, "clusters", name)
		writeFile(t, filepath.Join(dir, "flux-system", "kustomization.yaml"), "resources: [gotk-sync.yaml]\n")
		writeFile(t, filepath.Join(dir, "flux-system", "gotk-sync.yaml"), "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: flux-system\n  namespace: flux-system\nspec:\n  path: ./clusters/"+name+"\n")
		writeFile(t, filepath.Join(dir, "apps.yaml"), "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: apps\n  namespace: flux-system\nspec:\n  path: ./apps/"+name+"\n")
	}
	cluster("main")
	cluster("edge")
	writeFile(t, filepath.Join(root, "apps", "main", "kustomization.yaml"), "resources: [app.yaml, guarded.yaml, operator.yaml, templated.yaml, disabled.yaml, widgets.yaml]\n")
	writeFile(t, filepath.Join(root, "apps", "main", "guarded.yaml"), release("guarded", "guarded", "    trustCRDs: true\n"))
	writeFile(t, filepath.Join(root, "apps", "main", "app.yaml"), release("web", "app", app))
	writeFile(t, filepath.Join(root, "apps", "main", "operator.yaml"), release("operator", "operator", "    {}\n"))
	writeFile(t, filepath.Join(root, "apps", "main", "templated.yaml"), release("templated", "templated", "    crds: {enabled: true}\n"))
	writeFile(t, filepath.Join(root, "apps", "main", "disabled.yaml"), release("disabled", "disabled", "    crds: {enabled: false}\n"))
	writeFile(t, filepath.Join(root, "apps", "main", "widgets.yaml"), crd("plain.example.com"))
	writeFile(t, filepath.Join(root, "apps", "edge", "kustomization.yaml"), "resources: [app.yaml]\n")
	writeFile(t, filepath.Join(root, "apps", "edge", "app.yaml"), release("web", "app", app))

	out, stderr := runCLISplit(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json", "--fail-on", "none")
	got := map[string]anyCheck{}
	for _, c := range decodeChecks(t, out) {
		got[c.App] = c
	}
	status := func(app, key string) string {
		for _, k := range got[app].Keys {
			if k.Key == key {
				return k.Status
			}
		}
		return ""
	}
	for key, want := range map[string]string{"fromChartDir": "ALIVE", "fromTemplate": "ALIVE", "fromPlainFile": "ALIVE", "fromDisabled": "CONDITIONAL"} {
		if s := status("main:main:apps/web", key); s != want {
			t.Errorf("main %s = %s, want %s\n%v", key, s, want, got)
		}
	}
	if s := status("main:main:apps/guarded", "trustCRDs"); s != "ALIVE" {
		t.Errorf("trustCRDs = %s: it matters until the CRD exists, as on a first install", s)
	}
	for _, key := range []string{"fromChartDir", "fromTemplate", "fromPlainFile"} {
		if s := status("edge:edge:apps/web", key); s != "CONDITIONAL" {
			t.Errorf("edge %s = %s: a CRD of another cluster does not count", key, s)
		}
	}
	if !strings.Contains(stderr, "APIs served by CRDs in the repository: ") || !strings.Contains(stderr, "main: static.example.com/v1") {
		t.Errorf("stderr = %q", stderr)
	}

	out, _ = runCLISplit(t, "scan", filepath.Join(root, "apps", "main"), "--only", "web", "--repo-root", root, "-o", "json", "--fail-on", "none")
	only := decodeChecks(t, out)
	if len(only) != 1 {
		t.Fatalf("--only web: %+v", only)
	}
	for _, k := range only[0].Keys {
		if k.Key == "fromChartDir" && k.Status != "ALIVE" {
			t.Errorf("--only leaves the operator out of the report, not out of the cluster: %s", k.Status)
		}
	}
}

func TestScanKeepsOverlaysThatOnlyShareABase(t *testing.T) {
	root := t.TempDir()
	copyDir(t, "../../testdata/flux", root)
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	writeFile(t, filepath.Join(root, "apps", "staging", "kustomization.yaml"), "resources:\n  - ../base/demo\n")
	writeFile(t, filepath.Join(root, "apps", "production", "kustomization.yaml"), "resources:\n  - ../base/demo\n")
	writeFile(t, filepath.Join(root, "clusters", "main", "flux-system", "gotk-sync.yaml"), `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: flux-system
  namespace: flux-system
spec:
  path: ./clusters/main
`)
	writeFile(t, filepath.Join(root, "clusters", "main", "flux-system", "kustomization.yaml"), "resources: [gotk-sync.yaml]\n")
	writeFile(t, filepath.Join(root, "clusters", "main", "apps.yaml"), `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps
  namespace: flux-system
spec:
  path: ./apps/production
`)
	out, _ := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root, "-o", "json", "--fail-on", "none")
	found := false
	for _, c := range decodeChecks(t, out) {
		found = found || strings.HasPrefix(c.App, "staging:") && strings.Contains(c.Skipped, "not applied")
	}
	if !found {
		t.Errorf("the staging overlay reads only the base production also reads; it must be reported, not lost:\n%s", out)
	}
}

func TestChangedSinceComparesWhatAppsAreAnalyzedWith(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv := helmRepo(t)
	root := t.TempDir()
	app := func(name string) string {
		return `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: ` + name + `
  namespace: web
spec:
  chartRef:
    kind: HelmChart
    name: ` + name + `
    namespace: web
  values:
    name: app
    labels:
      team: ${TEAM}
`
	}
	chart := func(name, version string) string {
		return `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmChart
metadata:
  name: ` + name + `
  namespace: web
spec:
  chart: cases
  version: ` + version + `
  sourceRef:
    kind: HelmRepository
    name: charts
`
	}
	ks := func(name, comment string) string {
		return comment + `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: ` + name + `
  namespace: flux-system
spec:
  path: ./apps/` + name + `
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: settings-` + name + `
`
	}
	settings := func(name, team string) string {
		return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings-" + name + "\n  namespace: flux-system\ndata:\n  TEAM: " + team + "\n"
	}
	writeFile(t, filepath.Join(root, "apps", "source.yaml"), "apiVersion: source.toolkit.fluxcd.io/v1\nkind: HelmRepository\nmetadata:\n  name: charts\n  namespace: web\nspec:\n  url: "+srv.URL+"\n")
	writeFile(t, filepath.Join(root, "clusters", "main", "kustomization.yaml"), "resources: [flux-system, one.yaml, two.yaml, settings-one.yaml, settings-two.yaml]\n")
	writeFile(t, filepath.Join(root, "clusters", "main", "flux-system", "kustomization.yaml"), "resources: [gotk-sync.yaml]\n")
	writeFile(t, filepath.Join(root, "clusters", "main", "flux-system", "gotk-sync.yaml"), `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: flux-system
  namespace: flux-system
spec:
  path: ./clusters/main
`)
	for _, name := range []string{"one", "two"} {
		writeFile(t, filepath.Join(root, "apps", name, "kustomization.yaml"), "resources: [release.yaml, helmchart.yaml]\n")
		writeFile(t, filepath.Join(root, "apps", name, "release.yaml"), app(name))
		writeFile(t, filepath.Join(root, "apps", name, "helmchart.yaml"), chart(name, "0.1.0"))
		writeFile(t, filepath.Join(root, "clusters", "main", name+".yaml"), ks(name, ""))
		writeFile(t, filepath.Join(root, "clusters", "main", "settings-"+name+".yaml"), settings(name, "core"))
	}
	git := gitIn(t, root)
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-qm", "base")

	scanned := func(step string, want ...string) {
		t.Helper()
		out, stderr := runCLISplit(t, "scan", filepath.Join(root, "apps"), "--changed-since", "main", "--repo-root", root, "-o", "json", "--fail-on", "none")
		var got []string
		for _, c := range decodeChecks(t, out) {
			got = append(got, strings.TrimPrefix(c.App, "apps/"))
		}
		sort.Strings(got)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: apps = %v, want %v\n%s", step, got, want, stderr)
		}
		if len(want) == 0 && !strings.Contains(stderr, "no app changed since main") {
			t.Errorf("%s: stderr = %q", step, stderr)
		}
	}
	scanned("nothing changed")

	writeFile(t, filepath.Join(root, "clusters", "main", "one.yaml"), ks("one", "# a comment\n"))
	scanned("a comment in a parent does not change what an app is analyzed with")

	writeFile(t, filepath.Join(root, "apps", "one", "helmchart.yaml"), chart("one", "0.2.0"))
	scanned("a version in another file", "one:web/one")

	writeFile(t, filepath.Join(root, "clusters", "main", "settings-two.yaml"), settings("two", "platform"))
	scanned("a variable from a ConfigMap", "one:web/one", "two:web/two")
}
