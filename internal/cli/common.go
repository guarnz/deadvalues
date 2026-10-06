package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/guarnz/deadvalues/internal/argo"
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
		fs.StringVar(&t.version, "version", "", "chart version or semver range (default: latest)")
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
	// only restricts findings to keys set in these values files (normalized
	// paths), when the user asked about specific files with -f alone.
	only map[string]bool
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

func (g *globals) appJobs(app *argo.Application, root string, t *targetFlags) ([]job, error) {
	targets, err := app.Targets(root)
	if err != nil {
		return nil, &exitError{code: 2, err: err}
	}
	if len(targets) == 0 {
		return nil, &exitError{code: 2, err: fmt.Errorf("%s: no Helm source found", app.Name)}
	}
	var out []job
	for _, tg := range targets {
		set := values.New()
		for _, f := range tg.ValueFiles {
			if err := set.AddFile(f); err != nil {
				return nil, &exitError{code: 2, err: err}
			}
		}
		if tg.Values != "" {
			if err := set.AddYAML([]byte(tg.Values), app.Name+": helm.values"); err != nil {
				return nil, &exitError{code: 2, err: err}
			}
		}
		if tg.ValuesObj != nil {
			set.AddMap(tg.ValuesObj, app.Name+": helm.valuesObject")
		}
		for _, p := range tg.Parameters {
			expr := p.Name + "=" + strings.ReplaceAll(p.Value, ",", `\,`)
			if err := set.AddSet(expr, app.Name+": helm.parameters", p.ForceString); err != nil {
				return nil, &exitError{code: 2, err: err}
			}
		}
		if t != nil {
			if err := addExtra(set, t); err != nil {
				return nil, err
			}
		}
		j := job{
			name:        tg.Name,
			app:         app.Name,
			appFile:     app.File,
			ident:       "argo:" + tg.Name,
			sourceIndex: tg.SourceIndex,
			ref:         tg.Chart,
			release:     tg.Release,
			namespace:   tg.Namespace,
			vals:        set,
		}
		if t != nil && t.release != "" {
			j.release = t.release
		}
		if t != nil && t.namespace != "" {
			j.namespace = t.namespace
		}
		out = append(out, j)
	}
	return out, nil
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
	return render.New(c, render.Options{
		Release:     j.release,
		Namespace:   j.namespace,
		KubeVersion: g.kubeVersion,
		APIVersions: g.apiVersions,
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
		return nil, fmt.Errorf("%s: baseline render failed: %w", r.Label(), err)
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
	return a, nil
}

func (g *globals) ignorer(app string) func([]string) bool {
	return func(path []string) bool { return g.cfg.Ignored(app, path) }
}

func (g *globals) check(ctx context.Context, j job) *report.Check {
	a, err := g.analyze(ctx, j, j.ref)
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
