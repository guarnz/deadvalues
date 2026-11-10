package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/guarnz/deadvalues/internal/compare"
	"github.com/guarnz/deadvalues/internal/probe"
	"github.com/guarnz/deadvalues/internal/render"
	"github.com/guarnz/deadvalues/internal/report"
	"github.com/guarnz/deadvalues/internal/source"
	"github.com/guarnz/deadvalues/internal/values"
)

type targetFlags struct {
	app       string
	chart     string
	repo      string
	version   string
	values    []string
	set       []string
	release   string
	namespace string
	repoRoot  string
}

func (t *targetFlags) register(fs *pflag.FlagSet, withVersion bool) {
	fs.StringVar(&t.app, "app", "", "Argo CD Application or Flux HelmRelease manifest")
	fs.StringVar(&t.chart, "chart", "", `local chart directory or .tgz, repo/name from "helm repo add", chart name with --repo, or oci:// reference`)
	fs.StringVar(&t.repo, "repo", "", "Helm repository URL for --chart")
	if withVersion {
		fs.StringVar(&t.version, "version", "", "chart version, semver range or OCI digest (default: latest)")
	}
	fs.StringSliceVarP(&t.values, "values", "f", nil, "values files, merged in order; given alone, the chart is found from the Application, HelmRelease or chart directory that uses them")
	fs.StringArrayVar(&t.set, "set", nil, "extra values, same syntax as helm --set")
	fs.StringVar(&t.release, "release", "", `release name (default: from the manifest, or "release")`)
	fs.StringVarP(&t.namespace, "namespace", "n", "", `release namespace (default: from the manifest, or "default")`)
	fs.StringVar(&t.repoRoot, "repo-root", "", "repository root for $values/, sources and local charts (default: git top-level)")
}

// job is one chart + values pair to analyze.
type job struct {
	name        string
	app         string
	appFile     string
	ident       string
	sourceIndex int
	ref         source.Ref
	release     string
	namespace   string
	vals        *values.Set
	notes       []string
	// placeholders are value paths that exist only on the cluster.
	placeholders [][]string
	// only restricts findings to keys set in these values files (normalized
	// paths), when the user asked about specific files with -f alone.
	only map[string]bool
	// skip is why the target is reported without being analyzed, and skipKind its report kind.
	skip     string
	skipKind string
	// watch are more files whose change touches the target, such as kustomization files.
	watch []string
	// cluster groups the targets deployed together, for the CRDs they can use; empty when unknown.
	cluster string
	// paths are the directories an Application deploys, where its plain CRDs live.
	paths []string
	// kubeVersion and apiVersions are the app's own, as an Argo CD source can set them.
	kubeVersion string
	apiVersions []string
	// skipCRDs is set when the app does not apply the crds/ files of the chart.
	skipCRDs bool
	// skipSchema is set when the app renders without checking values.schema.json.
	skipSchema bool
	// installOnly is set when the app is always rendered as helm template does, as by Argo CD.
	installOnly bool
}

func (g *globals) jobs(t *targetFlags) ([]job, error) {
	switch {
	case t.app != "" && t.chart != "":
		return nil, usageErr("--app and --chart are mutually exclusive")
	case t.app != "":
		return g.manifestJobs(t.app, t)
	case t.chart == "" && len(t.values) > 0:
		return g.valuesJobs(t)
	case t.chart == "":
		return nil, usageErr("one of --app, --chart or -f is required")
	}

	ref, err := g.loader.ResolveAlias(source.Ref{Repo: t.repo, Chart: t.chart, Version: t.version})
	if err != nil {
		return nil, &exitError{code: 2, err: err}
	}
	set := values.New()
	if err := addExtra(set, t); err != nil {
		return nil, err
	}
	name := t.release
	if name == "" {
		name = chartName(ref.Chart)
	}
	return []job{{
		name:      name,
		app:       name,
		ref:       ref,
		release:   t.release,
		namespace: t.namespace,
		vals:      set,
	}}, nil
}

func addExtra(set *values.Set, t *targetFlags) error {
	for _, f := range t.values {
		if err := set.AddFile(f); err != nil {
			return &exitError{code: 2, err: err}
		}
	}
	for _, s := range t.set {
		if err := set.AddSet(s, "--set", false); err != nil {
			return &exitError{code: 2, err: err}
		}
	}
	return nil
}

func chartName(chart string) string {
	base := filepath.Base(strings.TrimSuffix(strings.TrimPrefix(chart, "oci://"), "/"))
	return strings.TrimSuffix(base, ".tgz")
}

type analysis struct {
	renderer *render.Renderer
	defaults map[string]interface{}
	result   *probe.Result
	took     time.Duration
	notes    []string
}

func (g *globals) renderer(ctx context.Context, j job, ref source.Ref) (*render.Renderer, error) {
	c, err := g.loader.Load(ctx, ref)
	if err != nil {
		return nil, err
	}
	kube, apis := g.kubeVersion, g.apiVersions
	if j.kubeVersion != "" && !g.kubeVersionFlag {
		kube = j.kubeVersion
	}
	if len(j.apiVersions) > 0 {
		apis = append(append([]string{}, g.flagAPIVersions...), j.apiVersions...)
	}
	return render.New(c, render.Options{
		Release:              j.release,
		Namespace:            j.namespace,
		KubeVersion:          kube,
		APIVersions:          apis,
		Placeholders:         j.placeholders,
		SkipCRDs:             j.skipCRDs,
		SkipSchemaValidation: j.skipSchema,
	})
}

func renderFunc(r *render.Renderer) probe.RenderFunc {
	return func(vals map[string]interface{}) (compare.Snapshot, error) {
		m, err := r.Render(vals)
		if err != nil {
			return nil, err
		}
		return compare.Flatten(m)
	}
}

func (g *globals) analyze(ctx context.Context, j job, ref source.Ref) (*analysis, error) {
	r, err := g.renderer(ctx, j, ref)
	if err != nil {
		return nil, err
	}
	declared := r.DeclaredAPIVersions()
	withoutAPIs := r.WithoutAPIVersions(declared)
	apis := g.repoAPIs(ctx, j, r.MissingAPIVersions())
	if len(apis) > 0 {
		r = r.WithAPIVersions(apis)
	}
	defaults, err := r.Defaults()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := probe.Run(probe.Options{
		Render:      renderFunc(r),
		User:        j.vals.Values,
		Defaults:    defaults,
		Workers:     g.workers,
		Logf:        g.logf(j.name + " " + r.Version()),
		MissingAPIs: r.MissingAPIVersions(),
		RenderWithAPIs: func(vals map[string]interface{}, apis []string) (compare.Snapshot, error) {
			return renderFunc(r.WithAPIVersions(apis))(vals)
		},
	})
	if err != nil {
		if len(j.placeholders) > 0 {
			return nil, fmt.Errorf("%s: baseline render failed with placeholders at %s for valuesFrom sources not in the repository (mark them optional or skip the app): %w",
				r.Label(), displayPaths(j.placeholders), err)
		}
		return nil, &renderError{label: r.Label(), err: err}
	}
	if len(apis) > 0 || len(declared) > 0 {
		keepBootstrapKeys(withoutAPIs, r, j.vals.Values, res)
	}
	if !j.installOnly && r.UsesUpgrade() {
		keepUpgradeKeys(r, j.vals.Values, res)
	}
	a := &analysis{renderer: r, defaults: defaults, result: res, took: time.Since(start)}

	dead, needsAPI := false, false
	for _, f := range res.Findings {
		if f.Status == probe.Dead {
			dead = true
		}
		if len(f.RequiresAPI) > 0 {
			needsAPI = true
		}
	}
	if needsAPI {
		a.notes = append(a.notes, "list the APIs your cluster serves under apiVersions in .deadvalues.yaml to resolve API-dependent keys")
	}
	if dead && r.UsesLookup() {
		a.notes = append(a.notes, "the chart uses lookup, which is empty offline; keys read only through it can look dead")
	}
	if missing := r.MissingDependencies(); dead && len(missing) > 0 {
		a.notes = append(a.notes, "dependencies could not be fetched ("+strings.Join(missing, ", ")+"); keys read only by them can look dead")
	}
	return a, nil
}

// renderError is a chart that loads but does not render with the values, as told by the chart:
// a fail, a required value or the schema.
type renderError struct {
	label string
	err   error
}

func (e *renderError) Error() string { return e.label + ": baseline render failed: " + e.err.Error() }

func (e *renderError) Unwrap() error { return e.err }

// networkFailure reports whether a render failed on the network, as a schema whose $ref is
// downloaded, and not on the values.
func networkFailure(err error) bool {
	msg := err.Error()
	for _, s := range []string{"HTTP request failed", "dial tcp", "connection refused", "no such host", "i/o timeout", "network is unreachable", "TLS handshake"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// keepBootstrapKeys revives dead keys that matter while a CRD is not there yet, such as
// cilium's trustCRDsExist on a first install: they are idle on a running cluster, not dead.
func keepBootstrapKeys(without, with *render.Renderer, vals map[string]interface{}, res *probe.Result) {
	withAPIs := renderFunc(with)
	// A failure that the APIs do not fix, as a schema that needs the key, is not about the CRD.
	reviveDeadKeys(renderFunc(without), vals, res, func(mutated map[string]interface{}) bool {
		_, err := withAPIs(mutated)
		return err == nil
	})
}

// keepUpgradeKeys revives dead keys that only an upgrade reads, such as openebs' preUpgradeHook:
// Flux and helm upgrade render them, helm template does not.
func keepUpgradeKeys(r *render.Renderer, vals map[string]interface{}, res *probe.Result) {
	reviveDeadKeys(renderFunc(r.AsUpgrade()), vals, res, func(map[string]interface{}) bool { return true })
}

// reviveDeadKeys marks alive each dead key whose removal changes what other renders, or breaks
// it when breaks agrees; nothing changes when other cannot render the values at all.
func reviveDeadKeys(other probe.RenderFunc, vals map[string]interface{}, res *probe.Result, breaks func(map[string]interface{}) bool) {
	if !slices.ContainsFunc(res.Findings, func(f probe.Finding) bool { return f.Status == probe.Dead }) {
		return
	}
	base, err := other(values.CopyMap(vals))
	if err != nil {
		return
	}
	again, err := other(values.CopyMap(vals))
	if err != nil {
		return
	}
	noise := compare.Noise(base, again)
	for i, f := range res.Findings {
		if f.Status != probe.Dead {
			continue
		}
		mutated := values.CopyMap(vals)
		values.Delete(mutated, f.Path)
		snap, err := other(values.CopyMap(mutated))
		if err == nil && compare.Equal(base, snap, noise) {
			continue
		}
		if err != nil && !breaks(mutated) {
			continue
		}
		res.Findings[i].Status = probe.Alive
	}
}

func displayPaths(paths [][]string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = values.Display(p)
	}
	return strings.Join(out, ", ")
}

func (g *globals) ignorer(app string) func([]string) bool {
	return func(path []string) bool { return g.cfg.Ignored(app, path) }
}

func (g *globals) check(ctx context.Context, j job) *report.Check {
	if j.skip != "" {
		c := report.NotAnalyzed(j.name, j.skip, j.skipKind)
		c.AppFile = j.appFile
		return c
	}
	a, err := g.analyze(ctx, j, j.ref)
	if reason, ok := source.Unavailable(err); ok {
		c := report.NotAnalyzed(j.name, reason, "")
		c.AppFile = j.appFile
		return c
	}
	if err != nil {
		c := report.Failed(j.name, err)
		c.AppFile = j.appFile
		return c
	}
	c := report.NewCheck(j.name, j.appFile, a.renderer.Label(), j.vals, a.result, g.ignorer(j.app), a.took)
	c.Notes = append(append([]string{}, j.notes...), a.notes...)
	if j.only != nil {
		c.Restrict(j.only, normPath)
	}
	return c
}

func checkExit(reports []*report.Check, failOn string) error {
	problems, failed := 0, false
	for _, r := range reports {
		if r.Error != "" {
			failed = true
		}
		problems += r.Problems(failOn)
	}
	switch {
	case failed:
		return &exitError{code: 2}
	case problems > 0:
		return &exitError{code: 1}
	}
	return nil
}
