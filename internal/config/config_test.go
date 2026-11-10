package config

import (
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, key string
		want         bool
	}{
		{"service.port", "service.port", true},
		{"service", "service.port", true},
		{"service.port", "service", false},
		{"*.podAnnotations", "main.podAnnotations", true},
		{"*.podAnnotations", "a.b.podAnnotations", false},
		{"**.podAnnotations", "a.b.podAnnotations", true},
		{"**.podAnnotations", "podAnnotations", true},
		{"main.*", "main.resources.limits", true},
		{"defaultRules.rules.kube*", "defaultRules.rules.kubeProxy", true},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, strings.Split(tt.key, ".")); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.key, got, tt.want)
		}
	}
}

func TestIgnoredPerApp(t *testing.T) {
	c := &Config{Ignore: []Ignore{
		{App: "n8n", Keys: []string{"strategy.rollingUpdate"}},
		{Keys: []string{"global"}},
	}}
	if !c.Ignored("n8n", "n8n", []string{"strategy", "rollingUpdate"}) {
		t.Error("app-scoped ignore not applied")
	}
	if c.Ignored("other", "n8n", []string{"strategy", "rollingUpdate"}) {
		t.Error("app-scoped ignore leaked to another app")
	}
	if !c.Ignored("other", "x", []string{"global", "domain"}) {
		t.Error("global ignore not applied")
	}
}

func TestIgnoredPerChart(t *testing.T) {
	c := &Config{Ignore: []Ignore{
		{Chart: "app-template", Keys: []string{"defaultPodOptions.securityContext"}},
		{App: "web", Chart: "app-template", Keys: []string{"service.main.ports"}},
	}}
	key := []string{"defaultPodOptions", "securityContext", "runAsUser"}
	if !c.Ignored("hermes", "app-template", key) || !c.Ignored("kopia", "app-template", key) {
		t.Error("a chart-scoped ignore applies to every app of the chart")
	}
	if c.Ignored("redis", "redis", key) {
		t.Error("a chart-scoped ignore leaked to another chart")
	}
	port := []string{"service", "main", "ports", "http"}
	if !c.Ignored("web", "app-template", port) || c.Ignored("api", "app-template", port) || c.Ignored("web", "other", port) {
		t.Error("with app and chart, both must match")
	}
}
