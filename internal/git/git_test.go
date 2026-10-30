package git

import "testing"

func TestSameRepository(t *testing.T) {
	for _, u := range []string{
		"https://github.com/acme/apps",
		"https://github.com/Acme/apps.git",
		"https://user:token@github.com/acme/apps/",
		"git@github.com:acme/apps.git",
		"ssh://git@github.com:22/acme/apps.git",
	} {
		if !SameRepository(u, "https://github.com/acme/apps") {
			t.Errorf("%s is the same repository as https://github.com/acme/apps (normalized %q)", u, normalize(u))
		}
	}
	if SameRepository("https://github.com/acme/apps", "https://github.com/acme/values") {
		t.Error("acme/apps and acme/values are different repositories")
	}
}

func TestSamePath(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"https://github.com/me/ops", "http://forgejo.svc.cluster.local:3000/me/ops.git", true},
		{"git@github.com:Me/Ops.git", "https://codeberg.org/me/ops", true},
		{"https://github.com/me/ops", "https://github.com/other/ops", false},
		{"https://github.com/me/ops", "", false},
	} {
		if got := SamePath(tt.a, tt.b); got != tt.want {
			t.Errorf("SamePath(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
