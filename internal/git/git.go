// Package git starts the git found in PATH, the one the user already runs.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Command prepares git with args in dir; every git deadvalues starts goes through here.
func Command(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}

// remote prepares git for a repository a manifest names: it never prompts, and a URL
// cannot run a command through `ext::`. GIT_ALLOW_PROTOCOL and GIT_SSH_COMMAND still win.
func remote(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_ALLOW_PROTOCOL") == "" {
		cmd.Env = append(cmd.Env, "GIT_ALLOW_PROTOCOL=https:http:ssh:git")
	}
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return cmd
}

// run returns what git printed, or the last line of its error.
func run(cmd *exec.Cmd) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if last := lines[len(lines)-1]; last != "" {
			return "", errors.New(last)
		}
		return "", fmt.Errorf("git %s: %w", cmd.Args[3], err)
	}
	return stdout.String(), nil
}

// LsRemote lists the refs of url that match the patterns, by name; a peeled tag is `name^{}`.
func LsRemote(ctx context.Context, url string, patterns ...string) (map[string]string, error) {
	out, err := run(remote(ctx, os.TempDir(), append([]string{"ls-remote", "--", url}, patterns...)...))
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if commit, name, ok := strings.Cut(line, "\t"); ok {
			refs[name] = commit
		}
	}
	return refs, nil
}

// Fetch checks out one commit of url into dir, shallow when the server allows it. Symbolic
// links stay plain files, so a repository cannot point a chart at the files of this machine.
func Fetch(ctx context.Context, url, commit, dir string) error {
	if _, err := run(remote(ctx, dir, "init", "-q")); err != nil {
		return err
	}
	if _, err := run(remote(ctx, dir, "fetch", "-q", "--depth", "1", "--", url, commit)); err != nil {
		if _, err := run(remote(ctx, dir, "fetch", "-q", "--", url, "+refs/*:refs/fetched/*")); err != nil {
			return err
		}
	}
	_, err := run(remote(ctx, dir, "-c", "core.symlinks=false", "checkout", "-q", "--detach", commit))
	return err
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
	return Normalize(a) == Normalize(b)
}

// SamePath reports whether two git URLs name the same owner/repository on any host,
// such as a GitHub repository and its mirror on a self-hosted forge.
func SamePath(a, b string) bool {
	_, pa, _ := strings.Cut(Normalize(a), "/")
	_, pb, _ := strings.Cut(Normalize(b), "/")
	return pa != "" && pa == pb
}

// Normalize reduces a git URL to host/path.
func Normalize(u string) string {
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
