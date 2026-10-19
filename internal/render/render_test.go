package render

import (
	"reflect"
	"strings"
	"testing"

	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
)

func TestWithGroupVersions(t *testing.T) {
	got := withGroupVersions([]string{"monitoring.coreos.com/v1/ServiceMonitor", "v1/Service", "apps/v1", "cert-manager.io/v1"})
	want := []string{"monitoring.coreos.com/v1/ServiceMonitor", "v1/Service", "apps/v1", "cert-manager.io/v1", "monitoring.coreos.com/v1", "v1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("withGroupVersions() = %v, want %v", got, want)
	}
}

func TestKindAPIServesItsGroupVersion(t *testing.T) {
	c := &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: chart.APIVersionV2, Name: "demo", Version: "0.1.0"},
		Templates: []*common.File{
			{Name: "templates/monitor.yaml", Data: []byte(`{{- if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1" }}kind: ServiceMonitor{{ end }}`)},
		},
	}
	for name, newRenderer := range map[string]func() (*Renderer, error){
		"config": func() (*Renderer, error) {
			return New(c, Options{APIVersions: []string{"monitoring.coreos.com/v1/ServiceMonitor"}})
		},
		"probe": func() (*Renderer, error) {
			r, err := New(c, Options{})
			if err != nil {
				return nil, err
			}
			return r.WithAPIVersions([]string{"monitoring.coreos.com/v1/ServiceMonitor"}), nil
		},
	} {
		r, err := newRenderer()
		if err != nil {
			t.Fatal(err)
		}
		out, err := r.Render(map[string]interface{}{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out["demo/templates/monitor.yaml"], "ServiceMonitor") {
			t.Errorf("%s: a group/version/Kind API should also serve its group/version, got %q", name, out)
		}
		if missing := r.MissingAPIVersions(); len(missing) != 0 {
			t.Errorf("%s: MissingAPIVersions() = %v, want none", name, missing)
		}
	}
}

func TestMissingAPIVersionsReadsBothForms(t *testing.T) {
	c := &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: chart.APIVersionV2, Name: "demo", Version: "0.1.0"},
		Templates: []*common.File{
			{Name: "templates/a.yaml", Data: []byte(`{{- if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1/ServiceMonitor" }}{{ end }}`)},
			{Name: "templates/_helpers.tpl", Data: []byte(`{{- if has "monitoring.coreos.com/v1/PodMonitor" $.Capabilities.APIVersions -}}true{{- end -}}`)},
			{Name: "templates/b.yaml", Data: []byte(`{{- if has "cert-manager.io/v1/Certificate" .Capabilities.APIVersions }}{{ end }}`)},
		},
	}
	r, err := New(c, Options{APIVersions: []string{"cert-manager.io/v1/Certificate"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"monitoring.coreos.com/v1/PodMonitor", "monitoring.coreos.com/v1/ServiceMonitor"}
	if got := r.MissingAPIVersions(); !reflect.DeepEqual(got, want) {
		t.Errorf("MissingAPIVersions() = %v, want %v", got, want)
	}
}
