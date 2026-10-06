package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/guarnz/deadvalues/internal/report"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	casesChart  = "../../testdata/charts/cases"
	casesValues = "../../testdata/values/cases.yaml"
)

func runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return runWithConfig(t, cfg, args...)
}

func runWithConfig(t *testing.T, cfg string, args ...string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	args = append(args, "--config", cfg, "--cache-dir", t.TempDir())
	code := run(BuildInfo{Version: "test"}, args, &buf)
	return buf.String(), code
}

type jsonKey struct {
	Key     string `json:"key"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
	Origins []struct {
		File string `json:"file"`
		Line int    `json:"line"`
	} `json:"origins"`
}

type jsonCheck struct {
	App    string    `json:"app"`
	Noise  int       `json:"noisyFields"`
	Keys   []jsonKey `json:"keys"`
	Counts map[string]int
}

func statuses(t *testing.T, out string) (jsonCheck, map[string]jsonKey) {
	t.Helper()
	var c jsonCheck
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	m := map[string]jsonKey{}
	for _, k := range c.Keys {
		m[k.Key] = k
	}
	return c, m
}

func TestCheckClassifiesEveryCase(t *testing.T) {
	out, code := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "--release", "demo", "-o", "json")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (dead keys present)\n%s", code, out)
	}
	c, keys := statuses(t, out)

	want := map[string]string{
		"name":                      "ERROR",
		"replicas":                  "ALIVE",
		"image.repository":          "REDUNDANT",
		"image.tag":                 "ALIVE",
		"service.type":              "REDUNDANT",
		"service.prot":              "DEAD",
		"labels.team":               "ALIVE",
		"secretKey":                 "REDUNDANT",
		"message":                   "ALIVE",
		"features":                  "GROUP",
		"ingress.enabled":           "REDUNDANT",
		"ingress.host":              "CONDITIONAL",
		"monitoring.serviceMonitor": "CONDITIONAL",
		"env":                       "ALIVE",
		"sub.size":                  "CONDITIONAL",
	}
	for key, status := range want {
		got, ok := keys[key]
		if !ok {
			t.Errorf("%s: no finding", key)
			continue
		}
		if got.Status != status {
			t.Errorf("%s: status = %s, want %s (%s)", key, got.Status, status, got.Detail)
		}
	}
	if len(keys) != len(want) {
		t.Errorf("got %d findings, want %d: %v", len(keys), len(want), keys)
	}
	if !strings.Contains(keys["secretKey"].Detail, "fallback") {
		t.Errorf("secretKey detail = %q, want template fallback", keys["secretKey"].Detail)
	}
	if !strings.Contains(keys["ingress.host"].Detail, "ingress.enabled") {
		t.Errorf("ingress.host detail = %q, want toggle", keys["ingress.host"].Detail)
	}
	if !strings.Contains(keys["sub.size"].Detail, "sub.enabled") {
		t.Errorf("sub.size detail = %q, want toggle", keys["sub.size"].Detail)
	}
	if !strings.Contains(keys["monitoring.serviceMonitor"].Detail, "serves monitoring.coreos.com/v1/ServiceMonitor") {
		t.Errorf("monitoring.serviceMonitor detail = %q, want the API that gates it", keys["monitoring.serviceMonitor"].Detail)
	}
	if c.Noise == 0 {
		t.Error("randAlphaNum password should produce noisy fields")
	}
	if o := keys["service.prot"].Origins; len(o) != 1 || o[0].Line != 8 {
		t.Errorf("service.prot origins = %+v, want line 8", o)
	}
}

func TestCheckAPIVersions(t *testing.T) {
	out, _ := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "-o", "json",
		"--api-versions", "monitoring.coreos.com/v1/ServiceMonitor")
	_, keys := statuses(t, out)
	if s := keys["monitoring.serviceMonitor"].Status; s != "ALIVE" {
		t.Errorf("monitoring.serviceMonitor = %s, want ALIVE with the API version available", s)
	}
}

var timing = regexp.MustCompile(`\d+ renders in [^,\n]+(, \d+ random fields ignored)?`)

func TestCheckTableGolden(t *testing.T) {
	out, _ := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "--release", "demo", "--color", "never", "--alive")
	out = timing.ReplaceAllString(out, "N renders in T")
	golden := filepath.Join("..", "..", "testdata", "golden", "check-cases.txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got := strings.ReplaceAll(out, "\r\n", "\n"); got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("table output differs from %s:\n%s", golden, got)
	}
}

func TestFailOnAndIgnore(t *testing.T) {
	if _, code := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "--fail-on", "none"); code != 0 {
		t.Errorf("--fail-on none: exit %d, want 0", code)
	}
	if _, code := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "--fail-on", "bogus"); code != 2 {
		t.Errorf("--fail-on bogus: exit %d, want 2", code)
	}

	cfg := filepath.Join(t.TempDir(), ".deadvalues.yaml")
	body := "ignore:\n  - keys:\n      - service.prot\n      - monitoring\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runWithConfig(t, cfg, "check", "--chart", casesChart, "-f", casesValues, "--color", "never")
	if code != 0 {
		t.Errorf("with ignores: exit %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "2 ignored") {
		t.Errorf("summary should count ignored keys:\n%s", out)
	}

	cfg2 := filepath.Join(t.TempDir(), ".deadvalues.yaml")
	if err := os.WriteFile(cfg2, []byte("failOn: none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := runWithConfig(t, cfg2, "check", "--chart", casesChart, "-f", casesValues); code != 0 {
		t.Errorf("failOn from config: exit %d, want 0", code)
	}
	if _, code := runWithConfig(t, cfg2, "check", "--chart", casesChart, "-f", casesValues, "--fail-on", "dead"); code != 1 {
		t.Errorf("--fail-on overrides config: exit %d, want 1", code)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{"check"},
		{"check", "--app", "a.yaml", "--chart", "b"},
		{"check", "--chart", casesChart, "-o", "xml"},
		{"check", "--chart", casesChart, "--color", "sometimes"},
		{"scan"},
	}
	for _, args := range cases {
		if _, code := runCLI(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestColor(t *testing.T) {
	args := []string{"check", "--chart", casesChart, "-f", casesValues}
	tests := []struct {
		name  string
		env   map[string]string
		flags []string
		want  bool
	}{
		{"auto without terminal", nil, nil, false},
		{"always", nil, []string{"--color", "always"}, true},
		{"FORCE_COLOR", map[string]string{"FORCE_COLOR": "1"}, nil, true},
		{"NO_COLOR beats FORCE_COLOR", map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}, nil, false},
		{"always beats NO_COLOR", map[string]string{"NO_COLOR": "1"}, []string{"--color", "always"}, true},
		{"--no-color beats always", nil, []string{"--color", "always", "--no-color"}, false},
		{"json never colored", nil, []string{"--color", "always", "-o", "json"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("FORCE_COLOR", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			out, _ := runCLI(t, append(append([]string{}, args...), tt.flags...)...)
			if got := strings.Contains(out, "\x1b["); got != tt.want {
				t.Errorf("colored = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExplain(t *testing.T) {
	out, code := runCLI(t, "explain", "replicas", "--chart", casesChart, "-f", casesValues, "-o", "json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var e struct {
		Changes []struct{ Object, Field, Before, After string }
	}
	if err := json.Unmarshal([]byte(out), &e); err != nil {
		t.Fatal(err)
	}
	if len(e.Changes) != 1 || e.Changes[0].Object != "Deployment/app" || e.Changes[0].Field != ".spec.replicas" ||
		e.Changes[0].Before != "3" || e.Changes[0].After != "1" {
		t.Errorf("changes = %+v", e.Changes)
	}

	if _, code := runCLI(t, "explain", "nope", "--chart", casesChart, "-f", casesValues); code != 2 {
		t.Errorf("unknown key: exit %d, want 2", code)
	}
}

func TestPrune(t *testing.T) {
	data, err := os.ReadFile(casesValues)
	if err != nil {
		t.Fatal(err)
	}
	vals := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(vals, data, 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := runCLI(t, "prune", "--chart", casesChart, "-f", vals, "--dry-run", "--color", "never")
	if code != 0 {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if after, _ := os.ReadFile(vals); string(after) != string(data) {
		t.Fatal("dry run modified the file")
	}
	if !strings.Contains(out, "-  prot: 8080") {
		t.Errorf("dry run diff should remove service.prot:\n%s", out)
	}

	if out, code := runCLI(t, "prune", "--chart", casesChart, "-f", vals, "--yes"); code != 0 {
		t.Fatalf("prune: exit %d\n%s", code, out)
	}
	after, _ := os.ReadFile(vals)
	for _, gone := range []string{"prot:", "repository: nginx", "secretKey:", "enabled: false"} {
		if strings.Contains(string(after), gone) {
			t.Errorf("%q should be pruned:\n%s", gone, after)
		}
	}
	for _, kept := range []string{"tag: \"1.28\"", "host: app.example.com", "size: large", "team: core", "  - name: A", "serviceMonitor: true"} {
		if !strings.Contains(string(after), kept) {
			t.Errorf("%q should be kept:\n%s", kept, after)
		}
	}

	if out, code := runCLI(t, "check", "--chart", casesChart, "-f", vals); code != 0 {
		t.Errorf("after prune, check should pass: exit %d\n%s", code, out)
	}
}

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

func TestHelpAndVersionOutsideTerminal(t *testing.T) {
	out, code := runCLI(t)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Contains(out, bannerArt[1]) {
		t.Error("the banner must not be printed when stdout is not a terminal")
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "Docs and issues: "+projectURL) {
		t.Errorf("root help should end with the docs link:\n%s", out)
	}

	for _, args := range [][]string{{"version"}, {"--version"}} {
		out, code := runCLI(t, args...)
		if code != 0 || !strings.HasPrefix(out, "deadvalues test\ncommit:") {
			t.Errorf("%v outside a terminal must stay script-friendly: exit %d\n%s", args, code, out)
		}
	}
	if _, code := runCLI(t, "nope"); code != 2 {
		t.Errorf("an unknown command must still fail: exit %d", code)
	}
}

func TestBanner(t *testing.T) {
	var b bytes.Buffer
	printBanner(&b, report.Painter{}, "0.1.0")
	out := b.String()
	for _, l := range bannerArt {
		if !strings.Contains(out, l) {
			t.Errorf("missing art line %q", l)
		}
	}
	if !strings.Contains(out, "HELM VALUES DEAD CODE DETECTOR · v0.1.0") {
		t.Errorf("tagline:\n%s", out)
	}
	for _, l := range bannerArt {
		if len(l) > 80 {
			t.Errorf("art must fit 80 columns: %d", len(l))
		}
	}
}

func TestCheckMarksKeysFromSharedFiles(t *testing.T) {
	root := t.TempDir()
	copyDir(t, casesChart, filepath.Join(root, "charts", "cases"))
	data, _ := os.ReadFile(casesValues)
	writeFile(t, filepath.Join(root, "values", "cases.yaml"), string(data))
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
          - $values/values/cases.yaml
`
	}
	writeFile(t, filepath.Join(root, "apps", "one.yaml"), app("one"))
	writeFile(t, filepath.Join(root, "apps", "two.yaml"), app("two"))

	out, code := runCLI(t, "check", "--app", filepath.Join(root, "apps", "one.yaml"), "--repo-root", root, "--color", "never")
	if code != 0 {
		t.Errorf("keys in a shared file must not fail a single-app check: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "SHARED") || !strings.Contains(out, "shared with two") || !strings.Contains(out, "in shared files (run scan)") {
		t.Errorf("expected SHARED keys pointing at the other app:\n%s", out)
	}

	if _, code := runCLI(t, "scan", filepath.Join(root, "apps"), "--repo-root", root); code != 1 {
		t.Errorf("scan sees every app, so a key no app uses is dead: exit %d", code)
	}
}

func copyDir(t *testing.T, src, dst string) {
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

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
