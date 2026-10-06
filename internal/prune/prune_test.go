package prune

import "testing"

func TestApply(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		paths [][]string
		want  string
		n     int
	}{
		{
			name:  "leaf keeps siblings, comments and quoting",
			in:    "# header\nimage:\n  repository: nginx # inline\n  tag: \"1.28\"\nreplicas: 3\n",
			paths: [][]string{{"image", "repository"}},
			want:  "# header\nimage:\n  tag: \"1.28\"\nreplicas: 3\n",
			n:     1,
		},
		{
			name:  "only child removes the parent",
			in:    "a: 1\nmonitoring:\n  serviceMonitor:\n    enabled: true\nb: 2\n",
			paths: [][]string{{"monitoring", "serviceMonitor", "enabled"}},
			want:  "a: 1\nb: 2\n",
			n:     1,
		},
		{
			name:  "block value with nested lines",
			in:    "strategy:\n  type: Recreate\n  rollingUpdate:\n    maxSurge: 1\n    maxUnavailable: 0\nx: 1\n",
			paths: [][]string{{"strategy", "rollingUpdate"}},
			want:  "strategy:\n  type: Recreate\nx: 1\n",
			n:     1,
		},
		{
			name:  "sequence at the key's own indentation",
			in:    "env:\n- name: A\n  value: \"1\"\n- name: B\nother: true\n",
			paths: [][]string{{"env"}},
			want:  "other: true\n",
			n:     1,
		},
		{
			name:  "blank line between sections survives",
			in:    "a:\n  b: 1\n  c: 2\n\nd: 3\n",
			paths: [][]string{{"a", "c"}},
			want:  "a:\n  b: 1\n\nd: 3\n",
			n:     1,
		},
		{
			name:  "multi-line string",
			in:    "script: |\n  echo one\n  echo two\nkeep: yes\n",
			paths: [][]string{{"script"}},
			want:  "keep: yes\n",
			n:     1,
		},
		{
			name:  "dotted key names",
			in:    "labels:\n  recurring-job-group.longhorn.io/backup: enabled\n  team: core\n",
			paths: [][]string{{"labels", "recurring-job-group.longhorn.io/backup"}},
			want:  "labels:\n  team: core\n",
			n:     1,
		},
		{
			name:  "missing path is a no-op",
			in:    "a: 1\n",
			paths: [][]string{{"b"}},
			want:  "a: 1\n",
			n:     0,
		},
		{
			name:  "CRLF line endings",
			in:    "a: 1\r\nb:\r\n  c: 2\r\n  d: 3\r\n",
			paths: [][]string{{"b", "c"}},
			want:  "a: 1\r\nb:\r\n  d: 3\r\n",
			n:     1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n, err := Apply([]byte(tt.in), edits(tt.paths))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tt.want)
			}
			if n != tt.n {
				t.Errorf("removed = %d, want %d", n, tt.n)
			}
		})
	}
}

func edits(paths [][]string) []Edit {
	out := make([]Edit, len(paths))
	for i, p := range paths {
		out[i] = Edit{Path: p}
	}
	return out
}

func TestApplyHelmReleaseValuesInMultiDocFile(t *testing.T) {
	in := `apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: app
spec:
  url: oci://example.com/app
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: app
spec:
  values:
    service:
      prot: 8080
    replicas: 2
---
kind: ConfigMap
metadata:
  name: other
`
	want := `apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: app
spec:
  url: oci://example.com/app
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: app
spec:
  values:
    replicas: 2
---
kind: ConfigMap
metadata:
  name: other
`
	got, n, err := Apply([]byte(in), []Edit{{Doc: 1, Path: []string{"spec", "values", "service", "prot"}}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || string(got) != want {
		t.Errorf("got (n=%d):\n%s", n, got)
	}
}

func TestApplyFlowMapping(t *testing.T) {
	got, n, err := Apply([]byte("labels: {a: 1, b: 2}\nx: 1\n"), edits([][]string{{"labels", "a"}}))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || string(got) != "labels: {b: 2}\nx: 1\n" {
		t.Errorf("got %q (n=%d)", got, n)
	}
}
