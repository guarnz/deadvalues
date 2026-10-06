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
	if !c.Ignored("n8n", []string{"strategy", "rollingUpdate"}) {
		t.Error("app-scoped ignore not applied")
	}
	if c.Ignored("other", []string{"strategy", "rollingUpdate"}) {
		t.Error("app-scoped ignore leaked to another app")
	}
	if !c.Ignored("other", []string{"global", "domain"}) {
		t.Error("global ignore not applied")
	}
}
