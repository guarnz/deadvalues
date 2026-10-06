package cli

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/probe"
	"github.com/guarnz/deadvalues/internal/prune"
	"github.com/guarnz/deadvalues/internal/report"
	"github.com/guarnz/deadvalues/internal/values"
)

func newPruneCmd(g *globals) *cobra.Command {
	var (
		t                  targetFlags
		only               string
		includeConditional bool
		dryRun, yes        bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove dead and redundant keys from a values file",
		Long: `Remove dead and redundant keys from a values file.

Edits the file in place, removing only the lines of each key, so comments,
ordering and formatting of the remaining keys are preserved. Always shows a
diff first.`,
		Example: `  deadvalues prune --app apps/example.yaml --dry-run
  deadvalues prune -f values.yaml
  deadvalues prune --chart bitnami/nginx -f values.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := g.noOutputFiles("prune"); err != nil {
				return err
			}
			if !slices.Contains([]string{"dead", "redundant", "all"}, only) {
				return usageErr("--only must be dead, redundant or all")
			}
			jobs, err := g.jobs(&t)
			if err != nil {
				return err
			}

			var shared map[string][]string
			if t.app != "" {
				root := t.repoRoot
				if root == "" {
					root = argo.RepoRoot(t.app)
				}
				shared = g.otherUsers(t.app, root)
			}

			// A key is removed from a file only if every analyzed app that
			// loads the file agrees it has no effect.
			type vote struct {
				file  string
				edit  prune.Edit
				key   string
				path  []string
				agree bool
			}
			votes := map[string]*vote{}
			var order []string
			type done struct {
				j   job
				res *probe.Result
			}
			var analyzed []done
			skipped := map[string]bool{}
			for _, j := range jobs {
				a, err := g.analyze(cmd.Context(), j, j.ref)
				if err != nil {
					return &exitError{code: 2, err: err}
				}
				analyzed = append(analyzed, done{j, a.result})
				ignored := g.ignorer(j.app)
				for _, f := range a.result.Findings {
					ok := prunable(f.Status, only, includeConditional) && !ignored(f.Path)
					for _, o := range j.vals.ExactOrigins(f.Path) {
						if !o.IsFile {
							if ok {
								fmt.Fprintf(g.stderr, "skip %s: set in %s, edit it manually\n", f.Key(), o.File)
							}
							continue
						}
						nf := normPath(o.File)
						if j.only != nil && !j.only[nf] {
							continue
						}
						if apps := shared[nf]; len(apps) > 0 {
							if ok && !skipped[nf] {
								skipped[nf] = true
								fmt.Fprintf(g.stderr, "skip %s: also loaded by %s; run `deadvalues prune -f %s` to judge it against every app\n",
									g.writer().Rel(o.File), strings.Join(apps, ", "), g.writer().Rel(o.File))
							}
							continue
						}
						path := append(append([]string{}, o.Prefix...), f.Path...)
						k := nf + "\x01" + strconv.Itoa(o.Doc) + "\x01" + values.Key(path)
						v, seen := votes[k]
						if !seen {
							v = &vote{file: o.File, edit: prune.Edit{Doc: o.Doc, Path: path}, key: f.Key(), path: f.Path, agree: true}
							votes[k] = v
							order = append(order, k)
						}
						v.agree = v.agree && ok
					}
				}
			}
			for _, d := range analyzed {
				ignored := g.ignorer(d.j.app)
				for _, k := range order {
					v := votes[k]
					if !v.agree || !loads(d.j, v.file) {
						continue
					}
					if f, ok := d.res.Find(v.path); !ok || !prunable(f.Status, only, includeConditional) || ignored(v.path) {
						v.agree = false
					}
				}
			}

			edits := map[string][]prune.Edit{}
			for _, k := range order {
				if v := votes[k]; v.agree {
					edits[v.file] = append(edits[v.file], v.edit)
				}
			}
			if len(edits) == 0 {
				fmt.Fprintln(g.stdout, "nothing to prune")
				return nil
			}

			files := make([]string, 0, len(edits))
			for f := range edits {
				files = append(files, f)
			}
			sort.Strings(files)

			w := g.writer()
			p := report.Painter{On: g.colorOn}
			total, written := 0, 0
			for _, file := range files {
				before, err := os.ReadFile(file)
				if err != nil {
					return &exitError{code: 2, err: err}
				}
				after, n, err := prune.Apply(before, dedupe(edits[file]))
				if err != nil {
					return &exitError{code: 2, err: fmt.Errorf("%s: %w", file, err)}
				}
				if n == 0 {
					continue
				}
				total += n
				printDiff(g, p, w.BaseDir, file, before, after)

				if dryRun {
					continue
				}
				if !yes {
					ok, err := confirm(fmt.Sprintf("Remove %d keys from %s? [y/N] ", n, file))
					if err != nil {
						return &exitError{code: 2, err: err}
					}
					if !ok {
						continue
					}
				}
				info, err := os.Stat(file)
				if err != nil {
					return &exitError{code: 2, err: err}
				}
				if err := os.WriteFile(file, after, info.Mode().Perm()); err != nil {
					return &exitError{code: 2, err: err}
				}
				written++
			}
			switch {
			case dryRun:
				fmt.Fprintf(g.stdout, "\n%d keys would be removed (dry run)\n", total)
			default:
				fmt.Fprintf(g.stdout, "\nremoved keys from %d of %d files\n", written, len(files))
			}
			return nil
		},
	}
	t.register(cmd.Flags(), true)
	cmd.Flags().StringVar(&only, "only", "all", "dead | redundant | all")
	cmd.Flags().BoolVar(&includeConditional, "include-conditional", false, "also remove CONDITIONAL keys")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the diff, do not write")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "write without confirmation")
	return cmd
}

func loads(j job, file string) bool {
	nf := normPath(file)
	for _, s := range j.vals.Sources {
		if normPath(s) == nf {
			return true
		}
	}
	return false
}

func prunable(s probe.Status, only string, includeConditional bool) bool {
	switch s {
	case probe.Dead:
		return only != "redundant"
	case probe.Redundant:
		return only != "dead"
	case probe.Conditional:
		return includeConditional && only != "redundant"
	}
	return false
}

func dedupe(edits []prune.Edit) []prune.Edit {
	seen := map[string]bool{}
	var out []prune.Edit
	for _, e := range edits {
		if k := strconv.Itoa(e.Doc) + "\x01" + values.Key(e.Path); !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

func printDiff(g *globals, p report.Painter, base, file string, before, after []byte) {
	rel := (&report.Writer{BaseDir: base}).Rel(file)
	text, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(before)),
		B:        difflib.SplitLines(string(after)),
		FromFile: "a/" + rel,
		ToFile:   "b/" + rel,
		Context:  2,
	})
	for _, line := range strings.SplitAfter(text, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			fmt.Fprint(g.stdout, p.Bold(line))
		case strings.HasPrefix(line, "+"):
			fmt.Fprint(g.stdout, p.Green(line))
		case strings.HasPrefix(line, "-"):
			fmt.Fprint(g.stdout, p.Red(line))
		case strings.HasPrefix(line, "@@"):
			fmt.Fprint(g.stdout, p.Cyan(line))
		default:
			fmt.Fprint(g.stdout, line)
		}
	}
}

func confirm(prompt string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("refusing to write without --yes in a non-interactive session")
	}
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
