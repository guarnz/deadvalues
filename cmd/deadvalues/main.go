package main

import (
	"os"
	"runtime/debug"

	"github.com/guarnz/deadvalues/internal/cli"
)

// Set by GoReleaser through -ldflags on release builds.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	fillFromBuildInfo()
	os.Exit(cli.Execute(cli.BuildInfo{Version: version, Commit: commit, Date: date}))
}

// fillFromBuildInfo covers binaries built without -ldflags. `go install
// ...@v0.1.0` records the module version (the tag), and `go build` in a git
// checkout records a pseudo-version plus the commit and its time.
func fillFromBuildInfo() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	for _, s := range info.Settings {
		switch {
		case s.Key == "vcs.revision" && commit == "none":
			commit = s.Value
			if len(commit) > 7 {
				commit = commit[:7]
			}
		case s.Key == "vcs.time" && date == "unknown":
			date = s.Value
		}
	}
}
