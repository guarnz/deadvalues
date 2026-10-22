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
