package cli

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/guarnz/deadvalues/internal/report"
)

const projectURL = "https://github.com/guarnz/deadvalues"

var bannerArt = []string{
	` ____  _____ _____ ____  _____ _____ __    _____ _____ _____`,
	`|    \|   __|  _  |    \|  |  |  _  |  |  |  |  |   __|   __|`,
	`|  |  |   __|     |  |  |  |  |     |  |__|  |  |   __|__   |`,
	`|____/|_____|__|__|____/ \___/|__|__|_____|_____|_____|_____|`,
}

// printBanner writes the ASCII art and the tagline with the version. It is
// only shown on a terminal, by the root help and by `version`.
func printBanner(w io.Writer, p report.Painter, version string) {
	for _, l := range bannerArt {
		fmt.Fprintln(w, p.Cyan(l))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, p.Bold("HELM VALUES DEAD CODE DETECTOR")+p.Dim(" · "+displayVersion(version)))
	fmt.Fprintln(w)
}

func displayVersion(v string) string {
	switch {
	case v == "":
		return "dev"
	case v[0] >= '0' && v[0] <= '9':
		return "v" + v
	default:
		return v
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
