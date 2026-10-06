package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/guarnz/deadvalues/internal/compare"
	"github.com/guarnz/deadvalues/internal/report"
	"github.com/guarnz/deadvalues/internal/values"
)

func newExplainCmd(g *globals) *cobra.Command {
	var t targetFlags
	cmd := &cobra.Command{
		Use:   "explain <key>",
		Short: "Show which manifest fields a key affects",
		Long: `Show which manifest fields a key affects.

Renders the chart with and without the key and prints every field that
differs, grouped by Kubernetes object.`,
		Example: `  deadvalues explain service.type --app apps/example.yaml
  deadvalues explain service.type -f values.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobs, err := g.jobs(&t)
			if err != nil {
				return err
			}
			var out []*report.Explain
			for _, j := range jobs {
				path, ok := values.Resolve(j.vals.Values, args[0])
				if !ok {
					continue
				}
				out = append(out, g.explain(cmd, j, path))
			}
			if len(out) == 0 {
				return usageErr("key %q is not set in the values", args[0])
			}
			if err := g.emit(func(w *report.Writer) error { return w.Explains(out) }); err != nil {
				return err
			}
			for _, e := range out {
				if e.Error != "" {
					return &exitError{code: 2}
				}
			}
			return nil
		},
	}
	t.register(cmd.Flags(), true)
	return cmd
}

func (g *globals) explain(cmd *cobra.Command, j job, path []string) *report.Explain {
	v, _ := values.Lookup(j.vals.Values, path)
	e := &report.Explain{App: j.name, Key: values.Display(path), Value: v}
	r, err := g.renderer(cmd.Context(), j, j.ref)
	if err != nil {
		e.Error = err.Error()
		return e
	}
	e.Chart = r.Label()
	render := renderFunc(r)

	base, err := render(values.CopyMap(j.vals.Values))
	if err != nil {
		e.Error = "baseline render failed: " + err.Error()
		return e
	}
	again, err := render(values.CopyMap(j.vals.Values))
	if err != nil {
		e.Error = "baseline render failed: " + err.Error()
		return e
	}
	without := values.CopyMap(j.vals.Values)
	values.Delete(without, path)
	snap, err := render(without)
	if err != nil {
		e.Error = fmt.Sprintf("removing %s breaks the render: %v", e.Key, err)
		return e
	}
	e.Changes = compare.Changes(base, snap, compare.Noise(base, again))
	return e
}
