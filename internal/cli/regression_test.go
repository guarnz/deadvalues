package cli

import (
	"bytes"
	"io"
	"testing"
)

const (
	vaultwardenChart = "../../testdata/charts/vaultwarden-0.46.2/vaultwarden"
	n8nChart         = "../../testdata/charts/n8n-1.24.40/n8n"
)

func TestRegressionRealCharts(t *testing.T) {
	out, _ := runCLI(t, "check", "--chart", vaultwardenChart, "-f", "../../testdata/values/vaultwarden.yaml", "--release", "vaultwarden", "-n", "apps", "-o", "json")
	_, keys := statuses(t, out)
	if k := keys["adminToken.existingSecretKey"]; k.Status != "REDUNDANT" {
		t.Errorf("vaultwarden adminToken.existingSecretKey = %+v, want REDUNDANT (template fallback)", k)
	}
	if k := keys["storage.data.labels.recurring-job-group.longhorn.io/backup"]; k.Status != "ALIVE" {
		t.Errorf("vaultwarden storage.data.labels = %+v, want ALIVE", k)
	}

	out, _ = runCLI(t, "check", "--chart", n8nChart, "-f", "../../testdata/values/n8n.yaml", "--release", "n8n", "-n", "apps", "-o", "json")
	_, keys = statuses(t, out)
	if k := keys["strategy.rollingUpdate"]; k.Status != "DEAD" {
		t.Errorf("n8n strategy.rollingUpdate = %+v, want DEAD", k)
	}
	if k := keys["main.persistence.size"]; k.Status != "ALIVE" {
		t.Errorf("n8n main.persistence.size = %+v, want ALIVE", k)
	}
}

func BenchmarkCheckN8n(b *testing.B) {
	args := []string{"check", "--chart", n8nChart, "-f", "../../testdata/values/n8n.yaml", "--fail-on", "none", "-o", "json"}
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		if code := run(BuildInfo{}, args, &buf); code != 0 {
			b.Fatalf("exit %d", code)
		}
		_, _ = io.Copy(io.Discard, &buf)
	}
}
