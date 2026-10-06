// Package render renders a chart client-side, the same way `helm template`
// does, without touching a cluster.
package render

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"helm.sh/helm/v4/pkg/chart/common"
	commonutil "helm.sh/helm/v4/pkg/chart/common/util"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/engine"

	"github.com/guarnz/deadvalues/internal/values"
)

type Options struct {
	Release     string
	Namespace   string
	KubeVersion string
	APIVersions []string
}

type Renderer struct {
	chart *chart.Chart
	opts  common.ReleaseOptions
	caps  *common.Capabilities
}

func New(c *chart.Chart, o Options) (*Renderer, error) {
	caps := common.DefaultCapabilities.Copy()
	if o.KubeVersion != "" {
		kv, err := common.ParseKubeVersion(o.KubeVersion)
		if err != nil {
			return nil, err
		}
		caps.KubeVersion = *kv
	}
	if len(o.APIVersions) > 0 {
		caps.APIVersions = append(append(common.VersionSet{}, caps.APIVersions...), o.APIVersions...)
	}
	if o.Release == "" {
		o.Release = "release"
	}
	if o.Namespace == "" {
		o.Namespace = "default"
	}
	return &Renderer{
		chart: c,
		opts:  common.ReleaseOptions{Name: o.Release, Namespace: o.Namespace, Revision: 1, IsInstall: true},
		caps:  caps,
	}, nil
}

func (r *Renderer) Label() string {
	return r.chart.Metadata.Name + " " + r.chart.Metadata.Version
}

func (r *Renderer) Name() string { return r.chart.Metadata.Name }

func (r *Renderer) Version() string { return r.chart.Metadata.Version }

func (r *Renderer) UsesLookup() bool { return usesLookup(r.chart) }

// Defaults returns the chart's values.yaml coalesced with its subcharts.
func (r *Renderer) Defaults() (map[string]interface{}, error) {
	vals, err := commonutil.CoalesceValues(clone(r.chart), map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	return vals.AsMap(), nil
}

// Render returns the rendered manifests keyed by template path, skipping
// NOTES.txt, partials and templates that render to nothing.
func (r *Renderer) Render(vals map[string]interface{}) (map[string]string, error) {
	c := clone(r.chart)
	if err := chartutil.ProcessDependencies(c, vals); err != nil {
		return nil, err
	}
	top, err := commonutil.ToRenderValues(c, vals, r.opts, r.caps)
	if err != nil {
		return nil, err
	}
	out, err := engine.Render(c, top)
	if err != nil {
		return nil, err
	}
	manifests := make(map[string]string, len(out))
	for name, content := range out {
		if strings.HasSuffix(name, "NOTES.txt") || strings.HasPrefix(path.Base(name), "_") {
			continue
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		manifests[name] = content
	}
	return manifests, nil
}

// clone copies the parts of a chart that dependency processing mutates
// (metadata dependencies, values, the subchart list), so one parsed chart can
// be rendered many times, concurrently, without re-reading it.
func clone(c *chart.Chart) *chart.Chart {
	md := *c.Metadata
	md.Dependencies = make([]*chart.Dependency, len(c.Metadata.Dependencies))
	for i, d := range c.Metadata.Dependencies {
		dc := *d
		md.Dependencies[i] = &dc
	}
	out := &chart.Chart{
		Raw:           c.Raw,
		Metadata:      &md,
		Lock:          c.Lock,
		Templates:     c.Templates,
		Values:        values.CopyMap(c.Values),
		Schema:        c.Schema,
		SchemaModTime: c.SchemaModTime,
		Files:         c.Files,
		ModTime:       c.ModTime,
	}
	deps := make([]*chart.Chart, 0, len(c.Dependencies()))
	for _, d := range c.Dependencies() {
		deps = append(deps, clone(d))
	}
	out.SetDependencies(deps...)
	return out
}

// WithAPIVersions returns a renderer for the same chart whose capabilities
// also include apis, as if the cluster served them.
func (r *Renderer) WithAPIVersions(apis []string) *Renderer {
	caps := r.caps.Copy()
	caps.APIVersions = append(append(common.VersionSet{}, r.caps.APIVersions...), apis...)
	return &Renderer{chart: r.chart, opts: r.opts, caps: caps}
}

var apiVersionsHas = regexp.MustCompile(`APIVersions\.Has\s+"([^"]+)"`)

// MissingAPIVersions lists the API versions the chart checks with
// .Capabilities.APIVersions.Has that the configured capabilities lack,
// leaving out versions Kubernetes removed before the rendered version.
func (r *Renderer) MissingAPIVersions() []string {
	seen := map[string]bool{}
	var out []string
	var walk func(c *chart.Chart)
	walk = func(c *chart.Chart) {
		for _, t := range c.Templates {
			for _, m := range apiVersionsHas.FindAllStringSubmatch(string(t.Data), -1) {
				v := m[1]
				if seen[v] || r.caps.APIVersions.Has(v) || removed(v, r.caps.KubeVersion) {
					continue
				}
				seen[v] = true
				out = append(out, v)
			}
		}
		for _, d := range c.Dependencies() {
			walk(d)
		}
	}
	walk(r.chart)
	sort.Strings(out)
	return out
}

func usesLookup(c *chart.Chart) bool {
	for _, t := range c.Templates {
		if strings.Contains(string(t.Data), "lookup ") || strings.Contains(string(t.Data), "(lookup") {
			return true
		}
	}
	for _, d := range c.Dependencies() {
		if usesLookup(d) {
			return true
		}
	}
	return false
}
