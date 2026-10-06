// Command gendocs writes the CLI reference in docs/cli from the cobra
// commands, so it never drifts from --help. Run it from the repository root.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra/doc"

	"github.com/guarnz/deadvalues/internal/cli"
)

func main() {
	dir := "docs/cli"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := gen(dir); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}

// gen clears the old pages first, so a removed command leaves no file behind.
func gen(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	old, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return err
	}
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	return doc.GenMarkdownTree(cli.DocsRoot(), dir)
}
