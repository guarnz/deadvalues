// Package render renders a chart client-side, the same way `helm template`
// does, without touching a cluster.
package render

import (
	"bytes"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"helm.sh/helm/v4/pkg/chart/common"
	commonutil "helm.sh/helm/v4/pkg/chart/common/util"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/engine"
	releaseutil "helm.sh/helm/v4/pkg/release/v1/util"
	"sigs.k8s.io/yaml"

	"github.com/guarnz/deadvalues/internal/values"
)

type Options struct {
	Release     string
	Namespace   string
	KubeVersion string
	APIVersions []string
	// Placeholders are paths set from values that exist only on the cluster.
	Placeholders [][]string
}

// Placeholder stands in for a value that exists only on the cluster.
const Placeholder = "deadvalues-placeholder"

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
		caps.APIVersions = append(append(common.VersionSet{}, caps.APIVersions...), withGroupVersions(o.APIVersions)...)
	}
	if o.Release == "" {
		o.Release = "release"
	}
	if o.Namespace == "" {
		o.Namespace = "default"
	}
	if len(o.Placeholders) > 0 {
		var err error
		if c, err = withPlaceholders(c, o.Placeholders); err != nil {
			return nil, err
		}
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

// Render returns the rendered manifests keyed by template path, skipping NOTES.txt,
// partials, Helm test hooks and templates that render to nothing.
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
		content = withoutTests(content)
		if strings.TrimSpace(content) == "" {
			continue
		}
		manifests[name] = content
	}
	return manifests, nil
}

// withoutTests drops the Helm test hooks of a rendered template, as `helm template
// --skip-tests` does: Argo CD never applies them, and their names are often random.
func withoutTests(content string) string {
	if !strings.Contains(content, "helm.sh/hook") {
		return content
	}
	docs := releaseutil.SplitManifests(content)
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return manifestIndex(keys[i]) < manifestIndex(keys[j]) })
	var kept []string
	for _, k := range keys {
		if !isTestHook(docs[k]) {
			kept = append(kept, docs[k])
		}
	}
	return strings.Join(kept, "\n---\n")
}

// manifestIndex is the position of a SplitManifests key, "manifest-<n>".
func manifestIndex(key string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(key, "manifest-"))
	return n
}

func isTestHook(doc string) bool {
	var m struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		return false
	}
	for _, event := range strings.Split(m.Metadata.Annotations["helm.sh/hook"], ",") {
		switch strings.TrimSpace(event) {
		case "test", "test-success", "test-failure":
			return true
		}
	}
	return false
}

// clone copies the chart parts that dependency processing mutates (dependencies,
// values, subcharts), so one parsed chart renders many times, concurrently.
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

// withPlaceholders sets the paths the chart has no default for, so the render
// takes the same template branch as on the cluster, where the value exists.
func withPlaceholders(c *chart.Chart, paths [][]string) (*chart.Chart, error) {
	coalesced, err := commonutil.CoalesceValues(clone(c), map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	defaults := coalesced.AsMap()
	out := clone(c)
	for _, p := range paths {
		if v, ok := values.Lookup(defaults, p); ok && v != nil && v != "" {
			continue
		}
		values.SetPath(out.Values, p, Placeholder)
	}
	return out, nil
}

// WithAPIVersions returns a renderer for the same chart whose capabilities
// also include apis, as if the cluster served them.
func (r *Renderer) WithAPIVersions(apis []string) *Renderer {
	caps := r.caps.Copy()
	caps.APIVersions = append(append(common.VersionSet{}, r.caps.APIVersions...), withGroupVersions(apis)...)
	return &Renderer{chart: r.chart, opts: r.opts, caps: caps}
}

// withGroupVersions adds the group/version of each group/version/Kind entry,
// as a cluster's discovery serves both forms.
func withGroupVersions(apis []string) []string {
	out := append([]string{}, apis...)
	for _, a := range apis {
		i := strings.LastIndex(a, "/")
		if i <= 0 || i == len(a)-1 || !unicode.IsUpper(rune(a[i+1])) {
			continue
		}
		out = append(out, a[:i])
	}
	return out
}

var (
	apiVersionsHas = regexp.MustCompile(`APIVersions\.Has\s+"([^"]+)"|\bhas\s+"([^"]+)"\s+\$?\.Capabilities\.APIVersions\b`)
	// apiVersionsHasExpr matches checks whose API is not a literal, like Has $apiVersion.
	apiVersionsHasExpr = regexp.MustCompile(`APIVersions\.Has\s+[^"\s]|\bhas\s+[^"\s][^\n]*?\$?\.Capabilities\.APIVersions\b`)
	// apiLiteral matches a quoted API of a dotted group, with or without a kind.
	apiLiteral = regexp.MustCompile(`"([a-z0-9-]+(?:\.[a-z0-9-]+)+/v[0-9]+(?:(?:alpha|beta)[0-9]+)?(?:/[A-Z][A-Za-z0-9]*)?)"`)
)

// MissingAPIVersions lists the APIs the chart checks for that the capabilities lack and still exist;
// when a check takes its API from a variable, every API quoted in the templates is a candidate.
// CRDFiles returns the crds/ files of the chart and its subcharts.
func (r *Renderer) CRDFiles() [][]byte {
	var out [][]byte
	for _, f := range r.chart.CRDs() {
		out = append(out, f.Data)
	}
	return out
}

// DeclaresCRD reports whether a template of the chart or a subchart may render a CRD of group.
func (r *Renderer) DeclaresCRD(group string) bool {
	var walk func(c *chart.Chart) bool
	walk = func(c *chart.Chart) bool {
		for _, t := range c.Templates {
			if bytes.Contains(t.Data, []byte("CustomResourceDefinition")) && bytes.Contains(t.Data, []byte(group)) {
				return true
			}
		}
		return slices.ContainsFunc(c.Dependencies(), walk)
	}
	return walk(r.chart)
}

func (r *Renderer) MissingAPIVersions() []string {
	var files [][]byte
	var walk func(c *chart.Chart)
	walk = func(c *chart.Chart) {
		for _, t := range c.Templates {
			files = append(files, t.Data)
		}
		for _, d := range c.Dependencies() {
			walk(d)
		}
	}
	walk(r.chart)

	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if seen[v] || r.caps.APIVersions.Has(v) || removed(v, r.caps.KubeVersion) {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	indirect := false
	for _, data := range files {
		for _, m := range apiVersionsHas.FindAllSubmatch(data, -1) {
			add(string(m[1]) + string(m[2]))
		}
		indirect = indirect || apiVersionsHasExpr.Match(data)
	}
	if indirect {
		for _, data := range files {
			for _, m := range apiLiteral.FindAllSubmatch(data, -1) {
				add(string(m[1]))
			}
		}
	}
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
