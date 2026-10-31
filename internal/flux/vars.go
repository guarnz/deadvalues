package flux

import (
	"bytes"
	"os"
	"regexp"
	"strings"

	"github.com/fluxcd/pkg/envsubst"
	"go.yaml.in/yaml/v3"
)

const substituteDisabled = "kustomize.toolkit.fluxcd.io/substitute: disabled"

// varExpr reads the variable of a ${...} and whether it brings its own fallback.
var varExpr = regexp.MustCompile(`^\$\{#?([A-Za-z_][A-Za-z0-9_]*)(:?[=+?-])?`)

// Substitute expands ${...} with Flux's own envsubst, where $$ escapes a dollar. Unknown
// variables without a fallback stay as text, since their value can come from the cluster.
func Substitute(data []byte, vars map[string]string) []byte {
	if !bytes.Contains(data, []byte("$")) {
		return data
	}
	var out bytes.Buffer
	for i := 0; i < len(data); {
		end := exprEnd(data, i)
		switch {
		case bytes.HasPrefix(data[i:], []byte("$$")):
			out.WriteByte('$')
			i += 2
		case end > 0:
			out.WriteString(expand(string(data[i:end]), vars))
			i = end
		default:
			out.WriteByte(data[i])
			i++
		}
	}
	return out.Bytes()
}

// substituteBuild applies Substitute to a build output, leaving out the objects
// that Flux leaves out with the substitute: disabled annotation or label.
func substituteBuild(out []byte, vars map[string]string) []byte {
	sep := []byte("\n---\n")
	docs := bytes.Split(out, sep)
	for i, doc := range docs {
		if !bytes.Contains(doc, []byte(substituteDisabled)) {
			docs[i] = Substitute(doc, vars)
		}
	}
	return bytes.Join(docs, sep)
}

// exprEnd returns where the ${...} starting at i ends, with the ones nested in it,
// or 0 when there is none on that line.
func exprEnd(data []byte, i int) int {
	if !bytes.HasPrefix(data[i:], []byte("${")) {
		return 0
	}
	depth := 0
	for j := i; j < len(data); j++ {
		switch {
		case data[j] == '\n':
			return 0
		case bytes.HasPrefix(data[j:], []byte("${")):
			depth++
			j++
		case data[j] == '}':
			if depth--; depth == 0 {
				return j + 1
			}
		}
	}
	return 0
}

func expand(expr string, vars map[string]string) string {
	m := varExpr.FindStringSubmatch(expr)
	if m == nil {
		return expr
	}
	if _, known := vars[m[1]]; !known && m[2] == "" {
		return expr
	}
	out, err := envsubst.Eval(expr, func(name string) (string, bool) {
		if v, ok := vars[name]; ok {
			return v, true
		}
		if name == m[1] {
			return "", false
		}
		return "${" + name + "}", true
	})
	if err != nil {
		return expr
	}
	return out
}

// Vars returns the postBuild variables of a Flux Kustomization, with notes on
// sources it could not read.
func (ix *Index) Vars(ks *Object) (map[string]string, []string) {
	return vars(ks, ix.Find)
}

// VarsIn reads the variables from the objects a cluster's builds output first, so each
// cluster gets its own ConfigMap of the same name, then from the repository.
func (ix *Index) VarsIn(ks *Object, scope *Index) (map[string]string, []string) {
	return vars(ks, func(kind, name, namespace, near string) *Object {
		if o := scope.Find(kind, name, namespace, near); o != nil {
			return o
		}
		return ix.Find(kind, name, namespace, near)
	})
}

func vars(ks *Object, find func(kind, name, namespace, near string) *Object) (map[string]string, []string) {
	vars := map[string]string{}
	var notes []string
	if from := Get(ks.Node, "spec", "postBuild", "substituteFrom"); from != nil && from.Kind == yaml.SequenceNode {
		for _, ref := range from.Content {
			kind, name := Str(ref, "kind"), Str(ref, "name")
			label := kind + " " + name
			o := find(kind, name, ks.Namespace, ks.File)
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
			vars[sub.Content[i].Value] = Str(sub.Content[i+1])
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
