package source

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"helm.sh/helm/v4/pkg/chart/loader"
	chart "helm.sh/helm/v4/pkg/chart/v2"

	"github.com/guarnz/deadvalues/internal/git"
)

const (
	gitTimeout = 5 * time.Minute
	// maxLocalDeps stops file:// dependencies that point at each other.
	maxLocalDeps = 8
)

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// UnavailableError is a chart that could not be fetched from git, such as a private
// repository without credentials; the app is reported as not analyzed, not as failed.
type UnavailableError struct{ Reason string }

func (e *UnavailableError) Error() string { return e.Reason }

// Unavailable returns why a chart could not be fetched, if that is what err says.
func Unavailable(err error) (string, bool) {
	var u *UnavailableError
	if errors.As(err, &u) {
		return u.Reason, true
	}
	return "", false
}

type checkout struct {
	once sync.Once
	dir  string
	err  error
}

// Checkout returns a clone of a repository at a revision, made once per run and kept in
// the cache by commit. Branches are looked up every run; a commit never changes.
func (l *Loader) Checkout(ctx context.Context, url, revision string) (string, error) {
	v, _ := l.clones.LoadOrStore(url+"\x00"+revision, &checkout{})
	c := v.(*checkout)
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(ctx, gitTimeout)
		defer cancel()
		c.dir, c.err = l.clone(ctx, url, revision)
	})
	return c.dir, c.err
}

func (l *Loader) clone(ctx context.Context, url, revision string) (string, error) {
	commit, err := resolveRevision(ctx, url, revision)
	if err != nil {
		return "", err
	}
	base := l.CacheDir
	if base == "" {
		if base, err = os.MkdirTemp("", "deadvalues-"); err != nil {
			return "", err
		}
	}
	// A short name, because Windows limits a path to 260 characters.
	dir := filepath.Join(base, "git", fmt.Sprintf("%x", sha256.Sum256([]byte(git.Normalize(url)+"@"+commit)))[:16])
	if l.NoCache {
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
	} else if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "clone-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := git.Fetch(ctx, url, commit, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", err
		}
	}
	return dir, nil
}

// resolveRevision turns a branch, tag, full ref or semver range into a commit, as
// Argo CD and Flux do; a name wins over a range, so a branch called `1.x` still works.
func resolveRevision(ctx context.Context, url, revision string) (string, error) {
	if fullCommit.MatchString(revision) {
		return revision, nil
	}
	if revision == "" {
		revision = "HEAD"
	}
	refs, err := git.LsRemote(ctx, url, revision)
	if err != nil {
		return "", err
	}
	for _, name := range []string{revision + "^{}", revision, "refs/tags/" + revision + "^{}", "refs/tags/" + revision, "refs/heads/" + revision} {
		if commit, ok := refs[name]; ok {
			return commit, nil
		}
	}
	c, ok := constraint(revision)
	if !ok {
		return "", fmt.Errorf("revision %q not found", revision)
	}
	refs, err = git.LsRemote(ctx, url, "refs/tags/*")
	if err != nil {
		return "", err
	}
	var tags []string
	for name := range refs {
		if tag, ok := strings.CutPrefix(name, "refs/tags/"); ok && !strings.HasSuffix(tag, "^{}") {
			tags = append(tags, tag)
		}
	}
	tag, ok := highest(tags, c)
	if !ok {
		return "", fmt.Errorf("no tag satisfies %q", revision)
	}
	if commit, ok := refs["refs/tags/"+tag+"^{}"]; ok {
		return commit, nil
	}
	return refs["refs/tags/"+tag], nil
}

func (l *Loader) gitChart(ctx context.Context, ref Ref) (*chart.Chart, error) {
	dir, err := l.Checkout(ctx, ref.Git, ref.Version)
	if err == nil {
		var c *chart.Chart
		if c, err = l.clonedChart(ctx, dir, filepath.FromSlash(strings.TrimPrefix(ref.Chart, "./")), 0); err == nil {
			return c, nil
		}
	}
	return nil, &UnavailableError{fmt.Sprintf("Helm chart %s could not be fetched: %v; for a private repository, give git access to it", ref, err)}
}

// clonedChart loads a chart of a clone with the dependencies its charts/ directory lacks,
// the ones Argo CD and Flux build before rendering: without them the chart is empty.
func (l *Loader) clonedChart(ctx context.Context, root, rel string, depth int) (*chart.Chart, error) {
	if !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("%s is outside the repository", rel)
	}
	if depth > maxLocalDeps {
		return nil, fmt.Errorf("%s: file:// dependencies go too deep", rel)
	}
	c, err := v2(loader.Load(filepath.Join(root, rel)))
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, d := range c.Dependencies() {
		have[d.Name()] = true
	}
	for _, d := range c.Metadata.Dependencies {
		if have[d.Name] {
			continue
		}
		var sub *chart.Chart
		if p, ok := strings.CutPrefix(d.Repository, "file://"); ok {
			sub, err = l.clonedChart(ctx, root, filepath.Join(rel, filepath.FromSlash(p)), depth+1)
		} else {
			sub, err = l.Load(ctx, Ref{Repo: d.Repository, Chart: d.Name, Version: lockedVersion(c, d)})
		}
		if err != nil {
			return nil, fmt.Errorf("dependency %s: %w", d.Name, err)
		}
		c.AddDependency(sub)
		have[d.Name] = true
	}
	return c, nil
}

// lockedVersion is the version Chart.lock pins for a dependency, or the one Chart.yaml asks for.
func lockedVersion(c *chart.Chart, d *chart.Dependency) string {
	if c.Lock != nil {
		for _, l := range c.Lock.Dependencies {
			if l.Name == d.Name && l.Repository == d.Repository {
				return l.Version
			}
		}
	}
	return d.Version
}
