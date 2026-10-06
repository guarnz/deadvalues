package cli

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// checkoutTree extracts the repository at rev into a temporary directory
// with `git archive`, without touching the working tree or the index.
func checkoutTree(root, rev string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "deadvalues-base-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	var stderr bytes.Buffer
	cmd := exec.Command("git", "-C", root, "archive", "--format=tar", rev)
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return "", nil, err
	}
	extractErr := extract(out, dir)
	// tar stops at its end-of-archive marker, but git keeps writing the
	// record padding after it. Drain the pipe so git can exit: on Windows
	// the pipe buffer is smaller than that padding and both sides would block.
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("git archive %s: %s", rev, strings.TrimSpace(stderr.String()))
	}
	if extractErr != nil {
		cleanup()
		return "", nil, extractErr
	}
	return dir, cleanup, nil
}

// resolveRef returns ref when git knows it, else origin/<ref> when that
// exists, the way `git switch` finds a branch that only exists on the remote.
func resolveRef(root, ref string) (string, error) {
	if refExists(root, ref) {
		return ref, nil
	}
	if !strings.HasPrefix(ref, "origin/") && refExists(root, "origin/"+ref) {
		return "origin/" + ref, nil
	}
	return "", fmt.Errorf("ref %q not found locally or on origin; run git fetch", ref)
}

func refExists(root, ref string) bool {
	return exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Run() == nil
}

// forkPoint returns the commit head branched from: its merge base with the
// repository's default branch (origin/HEAD, else origin/main, origin/master,
// main or master), which is what a pull request compares against.
func forkPoint(root, head string) (string, error) {
	var candidates []string
	if out, err := exec.Command("git", "-C", root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD").Output(); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(out)))
	}
	candidates = append(candidates, "origin/main", "origin/master", "main", "master")
	for _, c := range candidates {
		if exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", c).Run() != nil {
			continue
		}
		out, err := exec.Command("git", "-C", root, "merge-base", head, c).Output()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", fmt.Errorf("cannot tell where %s branched from; pass --base", head)
}

func extract(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(h.Name))
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(filepath.Separator)) {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}
