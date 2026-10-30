// Package git starts the git found in PATH, the one the user already runs.
package git

import (
	"os/exec"
	"strings"
	"sync"
)

// Command prepares git with args in dir; every git deadvalues starts goes through here.
func Command(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}

var lines sync.Map

// Line is the output of git args in dir, trimmed and cached, or "" when git fails.
func Line(dir string, args ...string) string {
	key := dir + "\x00" + strings.Join(args, "\x00")
	if v, ok := lines.Load(key); ok {
		return v.(string)
	}
	out, err := Command(dir, args...).Output()
	line := ""
	if err == nil {
		line = strings.TrimSpace(string(out))
	}
	lines.Store(key, line)
	return line
}

// Origin is the URL of the origin remote of the checkout at dir, or "" without one.
func Origin(dir string) string {
	return Line(dir, "remote", "get-url", "origin")
}

// SameRepository reports whether two git URLs name one repository, in https, ssh or scp form.
func SameRepository(a, b string) bool {
	return normalize(a) == normalize(b)
}

// SamePath reports whether two git URLs name the same owner/repository on any host,
// such as a GitHub repository and its mirror on a self-hosted forge.
func SamePath(a, b string) bool {
	_, pa, _ := strings.Cut(normalize(a), "/")
	_, pb, _ := strings.Cut(normalize(b), "/")
	return pa != "" && pa == pb
}

// normalize reduces a git URL to host/path.
func normalize(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	if _, rest, ok := strings.Cut(u, "://"); ok {
		u = rest
	}
	if i := strings.Index(u, "@"); i >= 0 && !strings.Contains(u[:i], "/") {
		u = u[i+1:]
	}
	host, p, _ := strings.Cut(strings.Replace(u, ":", "/", 1), "/")
	if first, rest, ok := strings.Cut(p, "/"); ok && first != "" && strings.Trim(first, "0123456789") == "" {
		p = rest
	}
	return host + "/" + strings.TrimSuffix(strings.Trim(p, "/"), ".git")
}
