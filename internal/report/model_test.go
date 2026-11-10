package report

import (
	"errors"
	"testing"

	"github.com/guarnz/deadvalues/internal/probe"
)

func TestDeadDetailOutlinesDefault(t *testing.T) {
	for _, tc := range []struct {
		def  interface{}
		want string
	}{
		{map[string]interface{}{"maxUnavailable": "25%", "maxSurge": "25%"}, `null  (chart default {maxSurge, maxUnavailable}; no effect with the current values)`},
		{[]interface{}{"a", "b"}, `null  (chart default [2 items]; no effect with the current values)`},
		{[]interface{}{"a"}, `null  (chart default [1 item]; no effect with the current values)`},
		{map[string]interface{}{}, `null  (chart default {}; no effect with the current values)`},
		{[]interface{}{}, `null  (chart default []; no effect with the current values)`},
		{"ClusterIP", `null  (chart default "ClusterIP"; no effect with the current values)`},
	} {
		f := probe.Finding{Path: []string{"k"}, Status: probe.Dead, Default: tc.def, HasDefault: true}
		if got := findingDetail(f); got != tc.want {
			t.Errorf("default %#v: detail = %q, want %q", tc.def, got, tc.want)
		}
	}
}

func TestAppVersion(t *testing.T) {
	for _, tc := range []struct{ from, to, want string }{
		{"1.20.1", "2.0.4", "app 1.20.1 → 2.0.4 (major)"},
		{"v1.20.1", "v1.21.0", "app v1.20.1 → v1.21.0"},
		{"2.0.4", "1.20.1", "app 2.0.4 → 1.20.1 (major)"},
		{"RELEASE.2024-01-01", "RELEASE.2025-01-01", "app RELEASE.2024-01-01 → RELEASE.2025-01-01"},
		{"1.20.1", "1.20.1", ""},
		{"", "2.0.4", ""},
	} {
		got := ""
		if a := NewAppVersion(tc.from, tc.to); a != nil {
			got = a.String()
		}
		if got != tc.want {
			t.Errorf("NewAppVersion(%q, %q) = %q, want %q", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestRenderFailureFindsTheTemplate(t *testing.T) {
	for _, tc := range []struct{ err, msg, template string }{
		{"execution error at (velero/templates/NOTES.txt:12:4): backupStorageLocation is now a list", "backupStorageLocation is now a list", "velero/templates/NOTES.txt:12"},
		{"template: app/templates/deployment.yaml:30:21: executing \"x\" at <.Values.a.b>: nil pointer", "executing \"x\" at <.Values.a.b>: nil pointer", "app/templates/deployment.yaml:30"},
		{"values don't meet the specifications of the schema(s) in the following chart(s):\napp:\n- at '/port': got string, want integer", "values don't meet the specifications of the schema(s) in the following chart(s): app: - at '/port': got string, want integer", "values.schema.json"},
		{"execution error at (velero/templates/NOTES.txt:78:4): \n#####\n###   BREAKING: options changed   ###\n#####\n\nERROR: make it a list\n", "BREAKING: options changed ERROR: make it a list", "velero/templates/NOTES.txt:78"},
		{"something else", "something else", ""},
		{"execution error at (app/templates/x.yaml:3:1): # is a comment\nsee issue #", "# is a comment see issue #", "app/templates/x.yaml:3"},
	} {
		msg, template := renderFailure(errors.New(tc.err))
		if msg != tc.msg || template != tc.template {
			t.Errorf("%q: got %q, %q", tc.err, msg, template)
		}
	}
}
