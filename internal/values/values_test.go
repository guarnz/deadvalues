package values

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMergeAndOrigins(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	os.WriteFile(a, []byte("image:\n  repository: nginx\n  tag: \"1.0\"\nreplicas: 1\n"), 0o644)
	os.WriteFile(b, []byte("image:\n  tag: \"2.0\"\n"), 0o644)

	s := New()
	if err := s.AddFile(a); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFile(b); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSet("replicas=3", "--set", false); err != nil {
		t.Fatal(err)
	}

	want := map[string]interface{}{
		"image":    map[string]interface{}{"repository": "nginx", "tag": "2.0"},
		"replicas": int64(3),
	}
	if !reflect.DeepEqual(s.Values, want) {
		t.Errorf("values = %#v", s.Values)
	}

	tag := s.Origins([]string{"image", "tag"})
	if len(tag) != 2 || tag[0].Line != 3 || tag[1].Line != 2 || !tag[1].IsFile {
		t.Errorf("image.tag origins = %+v", tag)
	}
	rep := s.Origins([]string{"replicas"})
	if len(rep) != 2 || rep[1].File != "--set" || rep[1].IsFile {
		t.Errorf("replicas origins = %+v", rep)
	}
}

func TestResolveDottedKeys(t *testing.T) {
	m := map[string]interface{}{
		"storage": map[string]interface{}{
			"labels": map[string]interface{}{"recurring-job-group.longhorn.io/backup": "enabled"},
		},
	}
	got, ok := Resolve(m, "storage.labels.recurring-job-group.longhorn.io/backup")
	want := []string{"storage", "labels", "recurring-job-group.longhorn.io/backup"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve = %v, %v", got, ok)
	}
	if _, ok := Resolve(m, "storage.nope"); ok {
		t.Error("missing key resolved")
	}
}

func TestLinesOfMergeKeys(t *testing.T) {
	s := New()
	data := "defaults: &defaults\n  port: 80\n  host: a\napp:\n  <<: *defaults\n  host: b\n"
	if err := s.AddYAML([]byte(data), "inline"); err != nil {
		t.Fatal(err)
	}
	for key, line := range map[string]int{"port": 2, "host": 6} {
		if o := s.Origins([]string{"app", key}); len(o) != 1 || o[0].Line != line {
			t.Errorf("%s: %+v, want line %d", key, o, line)
		}
	}
}

func TestLinesOfKeysWrittenAsAliases(t *testing.T) {
	s := New()
	if err := s.AddYAML([]byte("name: &app web\ncontrollers:\n  *app :\n    replicas: 1\n"), "inline"); err != nil {
		t.Fatal(err)
	}
	if o := s.Origins([]string{"controllers", "web", "replicas"}); len(o) != 1 || o[0].Line != 4 {
		t.Errorf("origins = %+v", o)
	}
}
