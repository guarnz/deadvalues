package probe

import (
	"reflect"
	"testing"

	"github.com/guarnz/deadvalues/internal/compare"
	"github.com/guarnz/deadvalues/internal/values"
)

// The sentinel must differ from the user value for every YAML type, or the
// REDUNDANT (implicit) test would miss template fallbacks for that type.
func TestSentinelDiffersFromValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value interface{}
		want  interface{}
	}{
		{"true", true, false},
		{"false", false, true},
		{"float", 3.5, 7922.5},
		{"int64", int64(2), int64(7921)},
		{"int", 2, 7921},
		{"list", []interface{}{"a"}, []interface{}{}},
		{"empty list", []interface{}{}, []interface{}{"deadvalues-sentinel"}},
		{"map", map[string]interface{}{"a": 1}, map[string]interface{}{"deadvalues-sentinel": "deadvalues-sentinel"}},
		{"string", "admin", "deadvalues-sentinel"},
		{"null", nil, "deadvalues-sentinel"},
	} {
		got := sentinel(tc.value)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: sentinel(%#v) = %#v, want %#v", tc.name, tc.value, got, tc.want)
		}
		if sameValue(got, tc.value) {
			t.Errorf("%s: sentinel(%#v) equals the value", tc.name, tc.value)
		}
	}
}

func TestLeafSentinel(t *testing.T) {
	got := leafSentinel([]interface{}{
		map[string]interface{}{"path": "/tmp", "readOnly": false, "subPaths": []interface{}{}},
		"x",
	})
	want := []interface{}{
		map[string]interface{}{"path": "deadvalues-sentinel", "readOnly": true, "subPaths": []interface{}{"deadvalues-sentinel"}},
		"deadvalues-sentinel",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("leafSentinel = %#v, want %#v", got, want)
	}
}

// A list equal to what the template renders without it (app-template mounts a
// volume at /<name> by default) is REDUNDANT, not DEAD.
func TestListMatchingTemplateFallback(t *testing.T) {
	render := func(v map[string]interface{}) (compare.Snapshot, error) {
		path := "/tmp"
		mounts, _ := values.Lookup(v, []string{"persistence", "tmp", "globalMounts"})
		if l, ok := mounts.([]interface{}); ok && len(l) > 0 {
			if m, ok := l[0].(map[string]interface{}); ok {
				if p, ok := m["path"].(string); ok {
					path = p
				}
			}
		}
		return compare.Snapshot{"mountPath": path}, nil
	}
	user := map[string]interface{}{
		"persistence": map[string]interface{}{
			"tmp":  map[string]interface{}{"globalMounts": []interface{}{map[string]interface{}{"path": "/tmp"}}},
			"temp": map[string]interface{}{"globalMounts": []interface{}{map[string]interface{}{"path": "/tmp"}}},
		},
	}
	res, err := Run(Options{Render: render, User: user, Defaults: map[string]interface{}{}})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]Status{
		"persistence.tmp.globalMounts":  Redundant,
		"persistence.temp.globalMounts": Dead,
	}
	for _, f := range res.Findings {
		if s, ok := want[f.Key()]; !ok || s != f.Status {
			t.Errorf("%s = %s, want %s", f.Key(), f.Status, s)
		}
		if f.Status == Redundant && !f.Implicit {
			t.Errorf("%s: want the template fallback, not the chart default", f.Key())
		}
	}
	if len(res.Findings) != len(want) {
		t.Errorf("got %d findings, want %d", len(res.Findings), len(want))
	}
}
