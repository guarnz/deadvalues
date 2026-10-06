package cli

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/report"
)

var checkLevels = []string{"dead", "conditional", "redundant", "none"}

func newCheckCmd(g *globals) *cobra.Command {
	var (
		t      targetFlags
		failOn string
		alive  bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report dead, conditional and redundant keys for one app",
		Long: `Report dead, conditional and redundant keys for one app.

The target is a GitOps manifest (--app: an Argo CD Application or a Flux
HelmRelease), a chart plus values files (--chart and -f), or just values
files (-f): their chart is then found from the app that uses them.`,
		Example: `  deadvalues check --app apps/example.yaml
  deadvalues check -f values.yaml
  deadvalues check --chart bitnami/nginx -f values.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			level, err := g.failOn(cmd, failOn, "dead", checkLevels)
			if err != nil {
				return err
			}
			jobs, err := g.jobs(&t)
			if err != nil {
				return err
			}
			var reports []*report.Check
			for _, j := range jobs {
				reports = append(reports, g.check(cmd.Context(), j))
			}
			report.ResolveShared(reports)
			if t.app != "" {
				root := t.repoRoot
				if root == "" {
					root = argo.RepoRoot(t.app)
				}
				users := g.otherUsers(t.app, root)
				for _, r := range reports {
					report.MarkShared(r, users, normPath)
				}
			}
			err = g.emit(func(w *report.Writer) error {
				w.Alive = alive
				return w.Checks(reports, false)
			})
			if err != nil {
				return err
			}
			return checkExit(reports, level)
		},
	}
	t.register(cmd.Flags(), true)
	cmd.Flags().StringVar(&failOn, "fail-on", "dead", "dead | conditional | redundant | none")
	cmd.Flags().BoolVar(&alive, "alive", false, "also list keys that have an effect")
	return cmd
}

// otherUsers maps every values file loaded by an app anywhere in the
// repository, Argo CD or Flux, other than the manifest self, to the names
// of those apps. Values embedded in manifests are left out: they belong to
// one release only.
func (g *globals) otherUsers(self, root string) map[string][]string {
	out := map[string][]string{}
	jobs, _, err := g.dirJobs(root, root, func(string) bool { return true })
	if err != nil {
		return out
	}
	selfPath := normPath(self)
	for _, j := range jobs {
		if normPath(j.appFile) == selfPath {
			continue
		}
		name := j.name
		if strings.HasPrefix(j.ident, "argo:") {
			name = j.app
		}
		for _, f := range j.vals.ValuesFiles() {
			k := normPath(f)
			if !slices.Contains(out[k], name) {
				out[k] = append(out[k], name)
			}
		}
	}
	return out
}
