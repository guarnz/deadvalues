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
			if abs, err := filepath.Abs(s); err == nil {
				f.skipDirs = append(f.skipDirs, abs)
			}
		}
	}
	return f
}

// in returns the filter with its directories moved by move, for a git
// revision extracted elsewhere.
func (f *appFilter) in(move func(string) string) *appFilter {
	if f == nil {
		return nil
	}
	out := *f
	out.skipDirs = make([]string, len(f.skipDirs))
	for i, d := range f.skipDirs {
		out.skipDirs[i] = move(d)
	}
	return &out
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
		d = normPath(d)
		if p == d || strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
