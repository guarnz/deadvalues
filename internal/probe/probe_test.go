package probe

import (
	"reflect"
	"testing"
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
