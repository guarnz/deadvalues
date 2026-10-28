package cli

import (
	"io"

	"github.com/spf13/cobra"
)

// DocsRoot returns the command tree for the generated CLI reference, with
// machine-dependent defaults replaced so the pages are the same everywhere.
func DocsRoot() *cobra.Command {
	root := newRoot(BuildInfo{Version: "dev"}, io.Discard, io.Discard)
	root.DisableAutoGenTag = true
	root.PersistentFlags().Lookup("workers").DefValue = "number of CPUs"
	return root
}
