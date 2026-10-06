package source

import "testing"

func TestConstraint(t *testing.T) {
	for _, tc := range []struct {
		version string
		isRange bool
	}{
		{"1.2.3", false},
		{"v1.2.3", false},
		{"latest", false},
		{"1.x", true},
		{">=1.0.0", true},
		{">=1.0.0 <2.0.0", true},
		{"*", true},
	} {
		if _, ok := constraint(tc.version); ok != tc.isRange {
			t.Errorf("constraint(%q) is a range = %v, want %v", tc.version, ok, tc.isRange)
		}
	}
}

func TestHighest(t *testing.T) {
	tags := []string{"0.9.0", "1.0.0", "1.4.2", "1.10.0", "2.0.0", "2.1.0-rc.1", "latest", "sha-abc"}
	for _, tc := range []struct {
		version, want string
		found         bool
	}{
		{"1.x", "1.10.0", true},
		{">=1.0.0 <2.0.0", "1.10.0", true},
		{"*", "2.0.0", true},
		{"~1.4.0", "1.4.2", true},
		{">=3.0.0", "", false},
	} {
		c, ok := constraint(tc.version)
		if !ok {
			t.Fatalf("constraint(%q) is not a range", tc.version)
		}
		got, found := highest(tags, c)
		if got != tc.want || found != tc.found {
			t.Errorf("highest(%q) = %q, %v; want %q, %v", tc.version, got, found, tc.want, tc.found)
		}
	}
}
