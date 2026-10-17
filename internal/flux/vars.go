package flux

import (
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Vars returns the postBuild variables of a Flux Kustomization, with notes on
// sources it could not read.
func (ix *Index) Vars(ks *Object) (map[string]string, []string) {
	vars := map[string]string{}
	var notes []string
	if from := Get(ks.Node, "spec", "postBuild", "substituteFrom"); from != nil && from.Kind == yaml.SequenceNode {
		for _, ref := range from.Content {
			kind, name := Str(ref, "kind"), Str(ref, "name")
			label := kind + " " + name
			o := ix.Find(kind, name, ks.Namespace, ks.File)
			switch {
			case o == nil && Str(ref, "optional") == "true":
			case o == nil:
				notes = append(notes, "variables from "+label+" are not in the repository and stay as ${...} text")
			case kind == "Secret" && Get(o.Node, "sops") != nil:
				notes = append(notes, "variables from "+label+" are SOPS-encrypted and stay as ${...} text")
			default:
				for k, v := range objectData(o) {
					vars[k] = v
				}
			}
		}
	}
	if sub := Get(ks.Node, "spec", "postBuild", "substitute"); sub != nil && sub.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(sub.Content); i += 2 {
			vars[sub.Content[i].Value] = sub.Content[i+1].Value
		}
	}
	return vars, notes
}

// objectData returns every data key of a ConfigMap or Secret.
func objectData(o *Object) map[string]string {
	out := map[string]string{}
	for k, file := range o.Gen {
		if data, err := os.ReadFile(file); err == nil {
			out[k] = strings.TrimRight(string(data), "\n")
		}
	}
	for _, field := range []string{"data", "stringData"} {
		m := Get(o.Node, field)
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			if v, ok := dataValue(o, m.Content[i].Value); ok {
				out[m.Content[i].Value] = v
			}
		}
	}
	return out
}
