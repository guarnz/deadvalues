// Package source obtains charts from a directory, a .tgz, an HTTP Helm
// repository or an OCI registry. Remote charts are loaded from memory and
// optionally cached as .tgz files.
package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	charter "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/loader"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/helmpath"
	"helm.sh/helm/v4/pkg/registry"
	repo "helm.sh/helm/v4/pkg/repo/v1"
	"sigs.k8s.io/yaml"
)

// v2 accepts the charts the Helm 4 SDK can render: apiVersion v1 and v2.
// apiVersion v3 charts load, but Helm itself does not install them through
// its public API yet.
func v2(c charter.Charter, err error) (*chart.Chart, error) {
	if err != nil {
		return nil, err
	}
	out, ok := c.(*chart.Chart)
	if !ok {
		return nil, fmt.Errorf("charts with apiVersion v3 are not supported yet")
	}
	return out, nil
}

// Ref identifies a chart. Repo is an HTTP(S) repository URL or an OCI
// registry path (with or without oci://, as Argo CD accepts both). With no
// Repo, Chart is a local directory, a local .tgz or an oci:// reference.
type Ref struct {
	Repo    string `json:"repo,omitempty"`
	Chart   string `json:"chart"`
	Version string `json:"version,omitempty"`
}

func (r Ref) String() string {
	s := r.Chart
	if r.Repo != "" {
		s = strings.TrimSuffix(r.Repo, "/") + "/" + r.Chart
	}
	if r.Version != "" {
		s += "@" + r.Version
	}
	return s
}

func (r Ref) WithVersion(v string) Ref {
	r.Version = v
	return r
}

func (r Ref) IsLocal() bool {
	return r.Repo == "" && !strings.HasPrefix(r.Chart, "oci://")
}

type Loader struct {
	CacheDir string
	NoCache  bool
	HTTP     *http.Client
	auth     map[string][2]string
}

func NewLoader(cacheDir string, noCache bool) *Loader {
	if cacheDir == "" {
		if dir, err := os.UserCacheDir(); err == nil {
			cacheDir = filepath.Join(dir, "deadvalues")
		}
	}
	return &Loader{CacheDir: cacheDir, NoCache: noCache, HTTP: &http.Client{Timeout: 2 * time.Minute}, auth: map[string][2]string{}}
}

// HelmRepositoryConfig is the repositories.yaml that `helm repo add` writes.
func HelmRepositoryConfig() string {
	if p := os.Getenv("HELM_REPOSITORY_CONFIG"); p != "" {
		return p
	}
	return helmpath.ConfigPath("repositories.yaml")
}

// ResolveAlias turns "repo/chart" into the repository registered with
// `helm repo add`, the way `helm template repo/chart` does. Local paths and
// oci:// references are returned unchanged; an existing local path wins.
func (l *Loader) ResolveAlias(ref Ref) (Ref, error) {
	if ref.Repo != "" || strings.HasPrefix(ref.Chart, "oci://") || strings.HasPrefix(ref.Chart, ".") || filepath.IsAbs(ref.Chart) {
		return ref, nil
	}
	if _, err := os.Stat(ref.Chart); err == nil {
		return ref, nil
	}
	alias, name, ok := strings.Cut(filepath.ToSlash(ref.Chart), "/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return ref, nil
	}
	f, err := repo.LoadFile(HelmRepositoryConfig())
	if err != nil {
		return ref, fmt.Errorf("chart %q is not a local path, and the Helm repository list could not be read (%s)", ref.Chart, HelmRepositoryConfig())
	}
	e := f.Get(alias)
	if e == nil {
		return ref, fmt.Errorf("chart %q is not a local path and there is no Helm repository named %q: run `helm repo add %s <url>` or pass --repo", ref.Chart, alias, alias)
	}
	if e.Username != "" {
		l.auth[strings.TrimSuffix(e.URL, "/")] = [2]string{e.Username, e.Password}
	}
	return Ref{Repo: e.URL, Chart: name, Version: ref.Version}, nil
}

func (l *Loader) Load(ctx context.Context, ref Ref) (*chart.Chart, error) {
	switch {
	case ref.IsLocal():
		return v2(loader.Load(ref.Chart))
	case strings.HasPrefix(ref.Chart, "oci://"):
		return l.oci(strings.TrimPrefix(ref.Chart, "oci://"), ref.Version)
	case isHTTP(ref.Repo):
		return l.http(ctx, ref)
	default:
		base := strings.TrimSuffix(strings.TrimPrefix(ref.Repo, "oci://"), "/")
		return l.oci(base+"/"+ref.Chart, ref.Version)
	}
}

func isHTTP(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func (l *Loader) oci(name, version string) (*chart.Chart, error) {
	if version == "" {
		version = "*"
	}
	client, err := registry.NewClient(registry.ClientOptCredentialsFile(helmpath.ConfigPath("registry/config.json")))
	if err != nil {
		return nil, err
	}
	if c, ok := constraint(version); ok {
		tags, err := client.Tags(name)
		if err != nil {
			return nil, fmt.Errorf("oci://%s: listing tags for %q: %w", name, version, err)
		}
		v, ok := highest(tags, c)
		if !ok {
			return nil, fmt.Errorf("oci://%s: no tag satisfies %q", name, version)
		}
		version = v
	}
	data, err := l.cached(name, version, func() ([]byte, error) {
		res, err := client.Pull(name+":"+version, registry.PullOptWithChart(true))
		if err != nil {
			return nil, err
		}
		return res.Chart.Data, nil
	})
	if err != nil {
		return nil, fmt.Errorf("oci://%s:%s: %w", name, version, err)
	}
	return v2(loader.LoadArchive(bytes.NewReader(data)))
}

// constraint parses version as a semver range ("1.x", ">=1.0.0 <2.0.0").
// Exact versions and plain tags such as "latest" are not ranges.
func constraint(version string) (*semver.Constraints, bool) {
	if _, err := semver.NewVersion(version); err == nil {
		return nil, false
	}
	c, err := semver.NewConstraint(version)
	if err != nil {
		return nil, false
	}
	return c, true
}

func highest(tags []string, c *semver.Constraints) (string, bool) {
	var best *semver.Version
	bestTag := ""
	for _, t := range tags {
		v, err := semver.NewVersion(t)
		if err != nil || !c.Check(v) {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best, bestTag = v, t
		}
	}
	return bestTag, best != nil
}

func (l *Loader) http(ctx context.Context, ref Ref) (*chart.Chart, error) {
	base := strings.TrimSuffix(ref.Repo, "/")
	if data, ok := l.readCache(base+"/"+ref.Chart, ref.Version); ok {
		return v2(loader.LoadArchive(bytes.NewReader(data)))
	}

	raw, err := l.get(ctx, base+"/index.yaml")
	if err != nil {
		return nil, err
	}
	var idx repo.IndexFile
	if err := yaml.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("%s/index.yaml: %w", base, err)
	}
	idx.SortEntries()
	cv, err := idx.Get(ref.Chart, ref.Version)
	if err != nil {
		return nil, fmt.Errorf("%s: chart %q version %q: %w", base, ref.Chart, ref.Version, err)
	}
	if len(cv.URLs) == 0 {
		return nil, fmt.Errorf("%s: chart %q version %q has no download URL", base, ref.Chart, cv.Version)
	}
	u, err := url.Parse(base + "/")
	if err != nil {
		return nil, err
	}
	target, err := u.Parse(cv.URLs[0])
	if err != nil {
		return nil, err
	}

	data, err := l.cached(base+"/"+ref.Chart, cv.Version, func() ([]byte, error) {
		return l.get(ctx, target.String())
	})
	if err != nil {
		return nil, err
	}
	return v2(loader.LoadArchive(bytes.NewReader(data)))
}

func (l *Loader) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for base, cred := range l.auth {
		if strings.HasPrefix(u, base) {
			req.SetBasicAuth(cred[0], cred[1])
		}
	}
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (l *Loader) cachePath(name, version string) string {
	name = strings.TrimPrefix(strings.TrimPrefix(name, "https://"), "http://")
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = unsafeChars.ReplaceAllString(p, "_")
	}
	file := parts[len(parts)-1] + "-" + unsafeChars.ReplaceAllString(version, "_") + ".tgz"
	return filepath.Join(append([]string{l.CacheDir}, append(parts[:len(parts)-1], file)...)...)
}

func (l *Loader) readCache(name, version string) ([]byte, bool) {
	if l.NoCache || l.CacheDir == "" || version == "" {
		return nil, false
	}
	data, err := os.ReadFile(l.cachePath(name, version))
	return data, err == nil
}

func (l *Loader) cached(name, version string, fetch func() ([]byte, error)) ([]byte, error) {
	if data, ok := l.readCache(name, version); ok {
		return data, nil
	}
	data, err := fetch()
	if err != nil {
		return nil, err
	}
	if !l.NoCache && l.CacheDir != "" {
		p := l.cachePath(name, version)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			_ = os.WriteFile(p, data, 0o644)
		}
	}
	return data, nil
}
