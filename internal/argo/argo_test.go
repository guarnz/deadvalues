package argo

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/guarnz/deadvalues/internal/git"
)

func TestValueFilesFromAnotherRepositoryAreNotAnalyzed(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:acme/apps.git"}} {
		if out, err := git.Command(root, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "envs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "envs", "web.yaml"), []byte("replicas: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := func(repo string) string {
		return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  sources:
    - repoURL: ` + repo + `
      ref: values
    - repoURL: https://charts.example.com
      chart: web
      targetRevision: 1.0.0
      helm:
        valueFiles:
          - $values/envs/web.yaml
`
	}
	for _, tt := range []struct {
		repo string
		skip bool
	}{
		{"https://github.com/acme/apps", false},
		{"https://github.com/acme/values.git", true},
	} {
		apps, err := Parse([]byte(app(tt.repo)))
		if err != nil {
			t.Fatal(err)
		}
		targets, err := apps[0].Targets(root)
		if err != nil {
			t.Fatal(err)
		}
		if got := targets[0].Skip; (got != "") != tt.skip || tt.skip && !strings.Contains(got, tt.repo) {
			t.Errorf("ref repo %s: skip %q, want skipped %v", tt.repo, got, tt.skip)
		}
	}
}

func TestNormalizeRepo(t *testing.T) {
	for _, u := range []string{
		"https://github.com/acme/apps",
		"https://github.com/Acme/apps.git",
		"https://user:token@github.com/acme/apps/",
		"git@github.com:acme/apps.git",
		"ssh://git@github.com:22/acme/apps.git",
	} {
		if got := normalizeRepo(u); got != "github.com/acme/apps" {
			t.Errorf("normalizeRepo(%q) = %q", u, got)
		}
	}
}

func TestBuildEnvInParametersAndValueFiles(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"chart/Chart.yaml", "chart/values/web.yaml", "envs/apps.yaml"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("name: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	apps, err := Parse([]byte(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  project: infra
  destination:
    namespace: apps
  sources:
    - repoURL: https://example.com/repo
      ref: values
    - repoURL: https://example.com/repo
      path: chart
      helm:
        valueFiles:
          - values/$ARGOCD_APP_NAME.yaml
          - $values/envs/${ARGOCD_APP_NAMESPACE}.yaml
        parameters:
          - name: tag
            value: $ARGOCD_APP_PROJECT_NAME-${ARGOCD_APP_NAME}-$$x-$UNKNOWN
`))
	if err != nil {
		t.Fatal(err)
	}
	targets, err := apps[0].Targets(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "chart", "values", "web.yaml"), filepath.Join(root, "envs", "apps.yaml")}
	if got := targets[0].ValueFiles; !reflect.DeepEqual(got, want) {
		t.Errorf("value files %v, want %v", got, want)
	}
	if got := targets[0].Parameters[0].Value; got != "infra-web-$x-" {
		t.Errorf("parameter %q, want %q", got, "infra-web-$x-")
	}
}

func TestHelmCapabilitiesReachTheTarget(t *testing.T) {
	apps, err := Parse([]byte(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  source:
    repoURL: https://charts.example.com
    chart: web
    targetRevision: 1.0.0
    helm:
      kubeVersion: "1.33"
      apiVersions:
        - monitoring.coreos.com/v1/ServiceMonitor
`))
	if err != nil {
		t.Fatal(err)
	}
	targets, err := apps[0].Targets(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := targets[0]; got.KubeVersion != "1.33" || !reflect.DeepEqual(got.APIVersions, []string{"monitoring.coreos.com/v1/ServiceMonitor"}) {
		t.Errorf("target kubeVersion %q, apiVersions %v", got.KubeVersion, got.APIVersions)
	}
}

func TestHelmNamespaceOverridesDestination(t *testing.T) {
	app := func(helm string) string {
		return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  destination:
    namespace: apps
  source:
    repoURL: https://charts.example.com
    chart: web
    targetRevision: 1.0.0
    helm:
      releaseName: web` + helm + `
`
	}
	for _, tt := range []struct{ helm, want string }{
		{"\n      namespace: monitoring", "monitoring"},
		{"", "apps"},
	} {
		apps, err := Parse([]byte(app(tt.helm)))
		if err != nil {
			t.Fatal(err)
		}
		targets, err := apps[0].Targets(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if len(targets) != 1 || targets[0].Namespace != tt.want {
			t.Errorf("helm block %q: namespace %q, want %q", tt.helm, targets[0].Namespace, tt.want)
		}
	}
}
