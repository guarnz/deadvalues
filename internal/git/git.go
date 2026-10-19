// Package git starts the git found in PATH, the one the user already runs.
package git

import "os/exec"

// Command prepares git with args in dir; every git deadvalues starts goes through here.
func Command(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}
