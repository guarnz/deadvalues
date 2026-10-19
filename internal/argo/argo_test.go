package argo

import (
	"reflect"
	"testing"
)

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
