package argo

import "testing"

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
