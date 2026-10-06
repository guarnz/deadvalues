package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/cobra"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/diff"
	"github.com/guarnz/deadvalues/internal/report"
	"github.com/guarnz/deadvalues/internal/source"
)

var diffLevels = []string{"breaking", "pinned", "changed", "none"}

func newDiffCmd(g *globals) *cobra.Command {
	var (
		t          targetFlags
		from, to   string
		base, head string
		failOn     string
	)
	cmd := &cobra.Command{
		Use:   "diff [dir]",
		Short: "Compare key status between two chart versions",
		Long: `Compare key status between two chart versions.

Runs check against the old and the new chart version with the same values
and reports what changed: keys that stopped working (BREAKING), keys whose
default changed under them (PINNED), defaults you do not set that now alter
the manifests (CHANGED) and dead keys that came back (FIXED).

With a directory, every Argo CD Application and Flux HelmRelease in it is
compared between two git revisions, and only the ones whose chart version
changed are diffed: --base is the "before" side, --head the "after" side.
Without --head the "after" side is your working tree; without --base it is
the commit --head branched from, as in a pull request. With -f alone, the
same is done for the Applications and HelmReleases that load those files.`,
		Example: `  deadvalues diff apps --head origin/renovate/example
  deadvalues diff -f config/example/values.yaml --head origin/renovate/example
  deadvalues diff apps --base origin/main
  deadvalues diff --app apps/example.yaml --to 0.2.0`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			level, err := g.failOn(cmd, failOn, "breaking", diffLevels)
			if err != nil {
				return err
			}

			var reports []*report.Diff
			if len(args) == 1 {
				if base == "" && head == "" {
					return usageErr("diff <dir> needs --base, --head or both")
				}
				if t.app != "" || t.chart != "" || from != "" || to != "" {
					return usageErr("diff <dir> cannot be combined with --app, --chart, --from or --to")
				}
				reports, err = g.diffDir(cmd.Context(), args[0], t.repoRoot, base, head)
				if err != nil {
					return &exitError{code: 2, err: err}
				}
				if note := downgradeNote(reports); note != "" {
					fmt.Fprintln(g.stderr, note)
				}
				if len(reports) == 0 {
					return g.emit(func(w *report.Writer) error {
						switch w.Format {
						case "json":
							fmt.Fprintln(w.Out, "[]")
						case "markdown":
							fmt.Fprint(w.Out, "No chart version changed.\n\n")
						case "table":
							fmt.Fprintln(w.Out, "no chart version changed")
						case "sarif":
							return w.Diffs(nil)
						}
						return nil
					})
				}
			} else {
				reports, err = g.diffTarget(cmd.Context(), &t, from, to, base, head)
				if err != nil {
					return err
				}
			}

			if err := g.emit(func(w *report.Writer) error { return w.Diffs(reports) }); err != nil {
				return err
			}
			problems, failed := 0, false
			for _, r := range reports {
				if r.Error != "" {
					failed = true
				}
				problems += r.Problems(level)
			}
			switch {
			case failed:
				return &exitError{code: 2}
			case problems > 0:
				return &exitError{code: 1}
			}
			return nil
		},
	}
	t.register(cmd.Flags(), false)
	cmd.Flags().StringVar(&from, "from", "", "old chart version (default: version in the manifest)")
	cmd.Flags().StringVar(&to, "to", "", "new chart version")
	cmd.Flags().StringVar(&base, "base", "", `git ref of the "before" side (default: where --head branched from)`)
	cmd.Flags().StringVar(&head, "head", "", `git ref of the "after" side (default: your working tree)`)
	cmd.Flags().StringVar(&failOn, "fail-on", "breaking", "breaking | pinned | changed | none")
	return cmd
}

// sides are the two trees a git-based diff compares: the "before" tree
// extracted at base, and the "after" tree, which is the working tree or a
// tree extracted at head.
type sides struct {
	root     string
	baseRoot string
	headRoot string
	baseRef  string
	cleanups []func()
}

func (g *globals) gitSides(root, base, head string) (*sides, error) {
	s := &sides{root: root, headRoot: root}
	var err error
	if base != "" {
		if base, err = resolveRef(root, base); err != nil {
			return nil, err
		}
	}
	if head != "" {
		if head, err = resolveRef(root, head); err != nil {
			return nil, err
		}
		dir, cleanup, err := checkoutTree(root, head)
		if err != nil {
			return nil, err
		}
		s.cleanups = append(s.cleanups, cleanup)
		s.headRoot = dir
		if base == "" {
			fork, err := forkPoint(root, head)
			if err != nil {
				s.close()
				return nil, err
			}
			base = fork
		}
	}
	dir, cleanup, err := checkoutTree(root, base)
	if err != nil {
		s.close()
		return nil, err
	}
	s.cleanups = append(s.cleanups, cleanup)
	s.baseRoot, s.baseRef = dir, base
	return s, nil
}

func (s *sides) close() {
	for _, c := range s.cleanups {
		c()
	}
}

func (s *sides) inBase(p string) string { return filepath.Join(s.baseRoot, relPath(s.root, p)) }

func (s *sides) inHead(p string) string { return filepath.Join(s.headRoot, relPath(s.root, p)) }

// relocate points a report built from the extracted head tree back at the
// repository's own paths.
func (s *sides) relocate(d *report.Diff) {
	if s.headRoot == s.root {
		return
	}
	back := func(p string) string {
		if r, err := filepath.Rel(s.headRoot, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.Join(s.root, r)
		}
		return p
	}
	d.AppFile = back(d.AppFile)
	for i := range d.Values {
		d.Values[i] = back(d.Values[i])
	}
	for i := range d.Changes {
		for j := range d.Changes[i].Origins {
			if o := &d.Changes[i].Origins[j]; o.IsFile {
				o.File = back(o.File)
			}
		}
	}
}

// diffTarget handles --app and --chart.
func (g *globals) diffTarget(ctx context.Context, t *targetFlags, from, to, base, head string) ([]*report.Diff, error) {
	gitMode := base != "" || head != ""
	if gitMode && t.app == "" {
		if t.chart != "" || len(t.values) == 0 {
			return nil, usageErr("--base and --head need --app, -f or a directory")
		}
		return g.diffValues(ctx, t, from, to, base, head)
	}

	target := *t
	var old map[string]job
	var s *sides
	if gitMode {
		root := t.repoRoot
		if root == "" {
			root = argo.RepoRoot(t.app)
		}
		var err error
		s, err = g.gitSides(root, base, head)
		if err != nil {
			return nil, &exitError{code: 2, err: err}
		}
		defer s.close()
		absApp, err := filepath.Abs(t.app)
		if err != nil {
			return nil, err
		}
		if head != "" {
			target.app, target.repoRoot = s.inHead(absApp), s.headRoot
			if _, err := os.Stat(target.app); err != nil {
				return nil, &exitError{code: 2, err: fmt.Errorf("%s does not exist at %s", t.app, head)}
			}
		}
		oldFile := s.inBase(absApp)
		if _, err := os.Stat(oldFile); err != nil {
			return nil, &exitError{code: 2, err: fmt.Errorf("%s does not exist at %s", t.app, s.baseRef)}
		}
		oldJobs, err := g.manifestJobs(oldFile, &targetFlags{repoRoot: s.baseRoot})
		if err != nil {
			return nil, err
		}
		old = byIdent(oldJobs)
	}

	jobs, err := g.jobs(&target)
	if err != nil {
		return nil, err
	}
	var reports []*report.Diff
	for _, j := range jobs {
		if j.ref.IsLocal() {
			if len(jobs) == 1 {
				return nil, usageErr("%s: diff needs a versioned chart from a repository, not a local path", j.name)
			}
			continue
		}
		oldRef, newRef := j.ref, j.ref
		if old != nil {
			o, ok := old[j.ident]
			if !ok {
				reports = append(reports, report.FailedDiff(j.name, fmt.Errorf("not found in %s at %s", t.app, s.baseRef)))
				continue
			}
			oldRef = o.ref
		}
		if from != "" {
			oldRef = oldRef.WithVersion(from)
		}
		if to != "" {
			newRef = newRef.WithVersion(to)
		}
		if old == nil && to == "" {
			return nil, usageErr("--to is required (or use --base / --head)")
		}
		reports = append(reports, g.diff(ctx, j, oldRef, newRef))
	}
	if s != nil {
		for _, d := range reports {
			s.relocate(d)
		}
	}
	return reports, nil
}

// diffValues handles -f alone with --base or --head: every Application or
// HelmRelease that loads the files is diffed as if it were given to --app,
// since only those manifests carry the chart version git can compare.
func (g *globals) diffValues(ctx context.Context, t *targetFlags, from, to, base, head string) ([]*report.Diff, error) {
	jobs, err := g.valuesJobs(t)
	if err != nil {
		return nil, err
	}
	var files []string
	seen := map[string]bool{}
	for _, j := range jobs {
		if j.appFile == "" {
			return nil, usageErr("%s: only a chart directory uses this file, so git has no chart version to compare; pass --chart with --from and --to", t.values[0])
		}
		if k := normPath(j.appFile); !seen[k] {
			seen[k] = true
			files = append(files, j.appFile)
		}
	}
	var reports []*report.Diff
	for _, f := range files {
		target := *t
		target.app, target.values = f, nil
		rs, err := g.diffTarget(ctx, &target, from, to, base, head)
		if err != nil {
			return nil, err
		}
		reports = append(reports, rs...)
	}
	return reports, nil
}

// diffDir compares every target under dir between the base and head trees.
func (g *globals) diffDir(ctx context.Context, dir, repoRoot, base, head string) ([]*report.Diff, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root := repoRoot
	if root == "" {
		root = argo.RepoRoot(filepath.Join(absDir, "x"))
	}
	s, err := g.gitSides(root, base, head)
	if err != nil {
		return nil, err
	}
	defer s.close()

	all := func(string) bool { return true }
	headDir := s.inHead(absDir)
	if _, err := os.Stat(headDir); err != nil {
		return nil, fmt.Errorf("%s does not exist at %s", dir, head)
	}
	current, failed, err := g.dirJobs(headDir, s.headRoot, all)
	if err != nil {
		return nil, err
	}
	var old map[string]job
	if baseDir := s.inBase(absDir); dirExists(baseDir) {
		oldJobs, _, err := g.dirJobs(baseDir, s.baseRoot, all)
		if err != nil {
			return nil, err
		}
		old = byIdent(oldJobs)
	}

	var reports []*report.Diff
	for _, f := range failed {
		d := report.FailedDiff(f.App, errors.New(f.Error))
		d.AppFile = f.AppFile
		reports = append(reports, d)
	}
	for _, j := range current {
		o, ok := old[j.ident]
		if !ok || j.ref.IsLocal() || o.ref.IsLocal() || o.ref == j.ref {
			continue
		}
		reports = append(reports, g.diff(ctx, j, o.ref, j.ref))
	}
	for _, d := range reports {
		s.relocate(d)
	}
	return reports, nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// downgradeNote warns when most chart versions go down: --base is the
// "before" side, and swapping the refs reads every bump backwards.
func downgradeNote(reports []*report.Diff) string {
	down, up := 0, 0
	example := ""
	for _, r := range reports {
		if r.Error != "" {
			continue
		}
		from, err1 := semver.NewVersion(r.From)
		to, err2 := semver.NewVersion(r.To)
		if err1 != nil || err2 != nil {
			continue
		}
		switch {
		case to.LessThan(from):
			down++
			if example == "" {
				example = fmt.Sprintf("%s %s → %s", r.App, r.From, r.To)
			}
		case to.GreaterThan(from):
			up++
		}
	}
	if down == 0 || down <= up {
		return ""
	}
	return fmt.Sprintf("note: chart versions go down in %d of %d targets (e.g. %s). --base is the \"before\" side and --head the \"after\" side: "+
		"to review a branch, pass it as --head, e.g. --head origin/renovate/<chart>", down, down+up, example)
}

func byIdent(jobs []job) map[string]job {
	m := map[string]job{}
	for _, j := range jobs {
		m[j.ident] = j
	}
	return m
}

func (g *globals) diff(ctx context.Context, j job, oldRef, newRef source.Ref) *report.Diff {
	start := time.Now()
	from, to := oldRef.Version, newRef.Version
	fail := func(err error) *report.Diff {
		d := report.FailedDiff(j.name, err)
		d.AppFile, d.From, d.To = j.appFile, from, to
		return d
	}
	if oldRef == newRef {
		r, err := g.renderer(ctx, j, newRef)
		if err != nil {
			return fail(err)
		}
		return report.NewDiff(j.name, j.appFile, r.Name(), from, to, j.vals, nil, 0, nil, time.Since(start))
	}

	oldA, err := g.analyze(ctx, j, oldRef)
	if err != nil {
		return fail(err)
	}
	newA, err := g.analyze(ctx, j, newRef)
	if err != nil {
		return fail(err)
	}
	from, to = oldA.renderer.Version(), newA.renderer.Version()

	ts := diff.Keys(oldA.result, newA.result, oldA.defaults, newA.defaults)
	changed, n := diff.ChangedDefaults(diff.ChangedOptions{
		OldDefaults: oldA.defaults,
		NewDefaults: newA.defaults,
		User:        j.vals.Values,
		Render:      renderFunc(newA.renderer),
		Base:        newA.result.Base,
		Noise:       newA.result.Noise,
		Workers:     g.workers,
	})
	ts = append(ts, changed...)
	diff.Sort(ts)

	renders := oldA.result.Renders + newA.result.Renders + n
	return report.NewDiff(j.name, j.appFile, newA.renderer.Name(), from, to, j.vals, ts, renders, g.ignorer(j.app), time.Since(start))
}
