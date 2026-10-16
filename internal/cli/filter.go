package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// appFilter applies --only and --skip. Apps match by any of their names, and
// --skip items that are directories skip everything under them.
type appFilter struct {
	only, skip []string
	skipDirs   []string
}

func newAppFilter(only, skip []string) *appFilter {
	f := &appFilter{only: only, skip: skip}
	for _, s := range skip {
		if info, err := os.Stat(s); err == nil && info.IsDir() {
			f.skipDirs = append(f.skipDirs, normPath(s))
		}
	}
	return f
}

// keep reports whether the app read from path, known by names, is analyzed.
func (f *appFilter) keep(path string, names ...string) bool {
	if f == nil {
		return true
	}
	if f.skipsDir(path) {
		return false
	}
	if len(f.only) > 0 && !slices.ContainsFunc(names, func(n string) bool { return slices.Contains(f.only, n) }) {
		return false
	}
	return !slices.ContainsFunc(names, func(n string) bool { return slices.Contains(f.skip, n) })
}

func (f *appFilter) skipsDir(path string) bool {
	if f == nil || path == "" {
		return false
	}
	p := normPath(path)
	for _, d := range f.skipDirs {
		if p == d || strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
