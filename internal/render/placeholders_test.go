package render

import (
	"strings"
	"testing"

	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
)

func TestPlaceholdersFillOnlyMissingDefaults(t *testing.T) {
	c := &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: chart.APIVersionV2, Name: "demo", Version: "0.1.0"},
		Values:   map[string]interface{}{"storage": map[string]interface{}{"port": 9000}},
		Templates: []*common.File{{Name: "templates/cm.yaml", Data: []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  bucket: {{ required "storage.bucket is required" .Values.storage.bucket | quote }}
  port: {{ .Values.storage.port | quote }}
`)}},
	}

	r, err := New(c, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Render(map[string]interface{}{}); err == nil || !strings.Contains(err.Error(), "storage.bucket is required") {
		t.Fatalf("without placeholders: err = %v, want the required error", err)
	}

	r, err = New(c, Options{Placeholders: [][]string{{"storage", "bucket"}, {"storage", "port"}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Render(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	cm := out["demo/templates/cm.yaml"]
	if !strings.Contains(cm, `bucket: "`+Placeholder+`"`) || !strings.Contains(cm, `port: "9000"`) {
		t.Errorf("rendered:\n%s\nwant the placeholder bucket and the chart default port", cm)
	}
	if _, ok := c.Values["storage"].(map[string]interface{})["bucket"]; ok {
		t.Error("the loaded chart was modified")
	}
}
