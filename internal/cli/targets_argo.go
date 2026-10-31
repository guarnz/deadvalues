package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/report"
	"github.com/guarnz/deadvalues/internal/values"
)

func appCluster(app *argo.Application) string {
	if app.Destination == "" {
		return ""
	}
	return "argo|" + app.Destination
}

func appPaths(app *argo.Application, root string) []string {
	var out []string
	for _, s := range app.Sources {
		if s.Path != "" {
			out = append(out, filepath.Join(root, filepath.FromSlash(s.Path)))
		}
	}
	return out
}

// kustomizeJobs analyzes the helmCharts of a kustomize directory that no Application
// points at, named after its path, as an ApplicationSet or plain kustomize renders it.
func (g *globals) kustomizeJobs(dir, root string, filter *appFilter) ([]job, []*report.Check) {
	file := argo.KustomizationFile(dir)
	rel := relPath(root, dir)
	if file == "" || filter.skipsDir(dir) || !filter.keep(file, rel) {
		return nil, nil
	}
	app := &argo.Application{Name: rel, File: file, Sources: []argo.Source{{Path: rel}}}
	js, err := g.appJobs(app, root, nil)
	if noHelmSource(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []*report.Check{failedAt(rel, file, err)}
	}
	return js, nil
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
		if tg.Skip != "" {
			out = append(out, job{name: tg.Name, app: app.Name, appFile: app.File, ident: "argo:" + tg.Name,
				sourceIndex: tg.SourceIndex, vals: values.New(), skip: tg.Skip})
			continue
		}
		set := values.New()
		for _, l := range tg.Layers {
			var err error
			if l.Node != nil {
				err = set.AddNode(l.Node, l.File, 0, l.Prefix)
			} else {
				err = set.AddFile(l.File)
			}
			if err != nil {
				return nil, &exitError{code: 2, err: err}
			}
		}
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
			cluster:     appCluster(app),
			paths:       appPaths(app, root),
			ref:         tg.Chart,
			release:     tg.Release,
			namespace:   tg.Namespace,
			vals:        set,
			notes:       tg.Notes,
			watch:       tg.Files,
			kubeVersion: tg.KubeVersion,
			apiVersions: tg.APIVersions,
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
