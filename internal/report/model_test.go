package report

import (
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
