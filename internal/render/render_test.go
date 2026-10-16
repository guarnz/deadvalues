package render

import (
	"reflect"
	"testing"

	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
)

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
