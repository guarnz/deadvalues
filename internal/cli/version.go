package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/guarnz/deadvalues/internal/report"
)

func newVersionCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit and Helm SDK version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := g.noOutputFiles("version"); err != nil {
				return err
			}
			g.printVersion()
			return nil
		},
	}
}

// printVersion backs both `deadvalues version` and `deadvalues --version`.
func (g *globals) printVersion() {
	details := fmt.Sprintf("commit:   %s\nbuilt:    %s\ngo:       %s\nhelm sdk: %s\n",
		g.info.Commit, g.info.Date, runtime.Version(), helmVersion())
	if isTerminal(g.stdout) {
		printBanner(g.stdout, report.Painter{On: g.colorOn}, g.info.Version)
		fmt.Fprint(g.stdout, details)
		return
	}
	fmt.Fprintf(g.stdout, "deadvalues %s\n%s", displayVersion(g.info.Version), details)
}

func helmVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, d := range info.Deps {
		if d.Path == "helm.sh/helm/v4" {
			return d.Version
		}
	}
	return "unknown"
}
