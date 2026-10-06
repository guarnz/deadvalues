package values

import "testing"

// FuzzAddYAML feeds arbitrary documents to the values parser: it must never
// panic, and every key it keeps must remember the file it came from.
func FuzzAddYAML(f *testing.F) {
	for _, seed := range []string{
		"",
		"a: 1\n",
		"a:\n  b: [1, 2]\n  c: {d: true}\n",
		"base: &b {x: 1}\nchild:\n  <<: *b\n  y: 2\n",
		"list: &l [1, 2]\nref: *l\n",
		"'dotted.key': v\n",
		"---\na: 1\n---\nb: 2\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s := New()
		if err := s.AddYAML(data, "values.yaml"); err != nil {
			return
		}
		Leaves(s.Values, nil, func(path []string, _ interface{}) {
			if len(s.Origins(path)) == 0 {
				t.Errorf("%q has no origin", Display(path))
			}
		})
	})
}
