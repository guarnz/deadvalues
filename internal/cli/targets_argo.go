package cli

import (
	"fmt"
	"strings"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/values"
)

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
