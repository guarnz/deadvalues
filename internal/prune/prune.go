// Package prune removes keys from a values file while keeping everything
// else byte-for-byte: comments, ordering, quoting and indentation.
package prune

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Edit is one key to remove: Path inside YAML document Doc of the file.
type Edit struct {
	Doc  int
	Path []string
}

// Apply removes every edit from data. A map left empty by a removal is
// removed as well. It returns the new content and how many keys were found.
func Apply(data []byte, edits []Edit) ([]byte, int, error) {
	removed := 0
	for _, e := range edits {
		out, ok, err := removeOne(data, e)
		if err != nil {
			return nil, removed, err
		}
		if ok {
			data = out
			removed++
		}
	}
	return data, removed, nil
}

type hit struct {
	key     *yaml.Node
	mapping *yaml.Node
	value   *yaml.Node
}

func parseDocs(data []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, &n)
	}
}

func removeOne(data []byte, e Edit) ([]byte, bool, error) {
	docs, err := parseDocs(data)
	if err != nil {
		return nil, false, err
	}
	if e.Doc >= len(docs) || len(docs[e.Doc].Content) == 0 || len(e.Path) == 0 {
		return data, false, nil
	}

	node := docs[e.Doc].Content[0]
	var chain []hit
	for _, seg := range e.Path {
		if node.Kind != yaml.MappingNode {
			return data, false, nil
		}
		found := false
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == seg {
				chain = append(chain, hit{key: node.Content[i], mapping: node, value: node.Content[i+1]})
				node = node.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return data, false, nil
		}
	}

	// Removing the only key of a nested map would leave `parent:` as null;
	// climb and remove the parent instead.
	idx := len(chain) - 1
	for idx > 0 && len(chain[idx].mapping.Content) == 2 {
		idx--
	}
	target := chain[idx]

	if inFlow(chain[:idx+1]) {
		return removeViaAST(docs, target)
	}
	return removeLines(data, target), true, nil
}

func inFlow(chain []hit) bool {
	for _, h := range chain {
		if h.mapping.Style&yaml.FlowStyle != 0 {
			return true
		}
	}
	return false
}

// removeLines deletes the key line and every following line that belongs to
// its value: deeper-indented lines, plus `- ` items at the key's own
// indentation when the value is a block sequence. Trailing blank lines are
// kept so the spacing between sections survives.
func removeLines(data []byte, h hit) []byte {
	lines := strings.SplitAfter(string(data), "\n")
	start := h.key.Line - 1
	indent := h.key.Column - 1
	isSeq := h.value.Kind == yaml.SequenceNode && h.value.Style&yaml.FlowStyle == 0

	end := start + 1
	for end < len(lines) {
		l := strings.TrimRight(lines[end], "\r\n")
		t := strings.TrimLeft(l, " ")
		if strings.TrimSpace(l) == "" {
			end++
			continue
		}
		if t == "---" || strings.HasPrefix(t, "--- ") {
			break
		}
		lead := len(l) - len(t)
		if lead < indent {
			break
		}
		if lead == indent && !(isSeq && strings.HasPrefix(t, "-")) {
			break
		}
		end++
	}
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}

	var b bytes.Buffer
	for i, l := range lines {
		if i < start || i >= end {
			b.WriteString(l)
		}
	}
	return b.Bytes()
}

func removeViaAST(docs []*yaml.Node, h hit) ([]byte, bool, error) {
	c := h.mapping.Content
	for i := 0; i+1 < len(c); i += 2 {
		if c[i] == h.key {
			h.mapping.Content = append(c[:i:i], c[i+2:]...)
			break
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return nil, false, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}
