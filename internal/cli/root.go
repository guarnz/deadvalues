// Package cli wires the deadvalues commands.
package cli

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/guarnz/deadvalues/internal/argo"
	"github.com/guarnz/deadvalues/internal/config"
	"github.com/guarnz/deadvalues/internal/flux"
	"github.com/guarnz/deadvalues/internal/report"
	"github.com/guarnz/deadvalues/internal/source"
)

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// exitError carries the process exit code: 1 for findings above the
// threshold, 2 for usage and runtime errors.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func usageErr(format string, args ...interface{}) error {
	return &exitError{code: 2, err: fmt.Errorf(format, args...)}
}

func Execute(info BuildInfo) int {
	return runIO(info, os.Args[1:], os.Stdout, os.Stderr)
}

func run(info BuildInfo, args []string, stdout io.Writer) int {
	return runIO(info, args, stdout, os.Stderr)
}

func runIO(info BuildInfo, args []string, stdout, stderr io.Writer) int {
	root := newRoot(info, stdout, stderr)
	root.SetArgs(args)
	root.SetOut(stdout)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(stderr, "error:", ee.err)
		}
		return ee.code
	}
	fmt.Fprintln(stderr, "error:", err)
	fmt.Fprintln(stderr, `Run "deadvalues --help" for usage.`)
	return 2
}

type globals struct {
	info        BuildInfo
	configPath  string
	output      string
	kubeVersion string
	apiVersions []string
	cacheDir    string
	noCache     bool
	workers     int
	color       string
	noColor     bool
	verbose     bool
	outputFiles []string
	outputs     []outputFile

	cfg       *config.Config
	loader    *source.Loader
	colorOn   bool
	cwd       string
	stdout    io.Writer
	stderr    io.Writer
	fluxIndex map[string]*flux.Index
}

func newRoot(info BuildInfo, stdout, stderr io.Writer) *cobra.Command {
	g := &globals{info: info, stdout: stdout, stderr: stderr}
	root := &cobra.Command{
		Use:   "deadvalues",
		Short: "Find Helm values that have no effect on the rendered chart",
		Long: `deadvalues finds Helm values that have no effect on the rendered chart:
typos, keys a chart renamed or removed, and keys equal to the chart default.

It renders the chart, removes each key you set and checks whether the
output changes. No template parsing, no regex.`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error { return g.setup(cmd) },
	}

	f := root.PersistentFlags()
	f.StringVarP(&g.configPath, "config", "c", "", "path to .deadvalues.yaml (default: repo root)")
	f.StringVarP(&g.output, "output", "o", "table", "table | json | markdown | sarif | github")
	f.StringVar(&g.kubeVersion, "kube-version", "", "Kubernetes version for .Capabilities (default: Helm SDK default)")
	f.StringSliceVar(&g.apiVersions, "api-versions", nil, "extra API versions for .Capabilities.APIVersions.Has")
	f.StringVar(&g.cacheDir, "cache-dir", "", `chart cache (default "<user cache dir>/deadvalues")`)
	f.BoolVar(&g.noCache, "no-cache", false, "always download charts")
	f.IntVarP(&g.workers, "workers", "w", runtime.NumCPU(), "concurrent renders")
	f.StringVar(&g.color, "color", "auto", "auto | always | never")
	f.BoolVar(&g.noColor, "no-color", false, "same as --color=never")
	f.BoolVarP(&g.verbose, "verbose", "v", false, "log every render and probe")
	f.StringArrayVar(&g.outputFiles, "output-file", nil, "write the report to a file in UTF-8 instead of stdout; FORMAT=FILE writes that format and keeps stdout (repeatable)")

	var showVersion bool
	root.Flags().BoolVar(&showVersion, "version", false, "print version, commit and Helm SDK version")
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		if showVersion {
			if err := g.noOutputFiles("--version"); err != nil {
				return err
			}
			g.printVersion()
			return nil
		}
		return cmd.Help()
	}

	root.AddCommand(
		newCheckCmd(g),
		newDiffCmd(g),
		newScanCmd(g),
		newExplainCmd(g),
		newPruneCmd(g),
		newVersionCmd(g),
	)

	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		isRoot := !c.HasParent()
		if isRoot && isTerminal(g.stdout) {
			on := g.decideColor(c.Flags().Changed("color"))
			if on {
				enableVirtualTerminal()
			}
			printBanner(c.OutOrStdout(), report.Painter{On: on}, g.info.Version)
		}
		defaultHelp(c, args)
		if isRoot {
			fmt.Fprintf(c.OutOrStdout(), "\nDocs and issues: %s\n", projectURL)
		}
	})
	return root
}

func (g *globals) setup(cmd *cobra.Command) error {
	if !slices.Contains(report.Formats, g.output) {
		return usageErr("--output must be one of %s", strings.Join(report.Formats, ", "))
	}
	if !slices.Contains([]string{"auto", "always", "never"}, g.color) {
		return usageErr("--color must be auto, always or never")
	}
	if g.workers < 1 {
		return usageErr("--workers must be at least 1")
	}
	g.outputs = parseOutputFiles(g.outputFiles, g.output)

	if g.verbose {
		log.SetOutput(g.stderr)
	} else {
		log.SetOutput(io.Discard)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	g.cwd = cwd

	cfgPath, required := g.configPath, true
	if cfgPath == "" {
		cfgPath, required = config.Default(argo.RepoRoot(filepath.Join(cwd, "x"))), false
	}
	g.cfg, err = config.Load(cfgPath, required)
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	if !cmd.Flags().Changed("kube-version") && g.cfg.KubeVersion != "" {
		g.kubeVersion = g.cfg.KubeVersion
	}
	g.apiVersions = append(g.apiVersions, g.cfg.APIVersions...)

	g.loader = source.NewLoader(g.cacheDir, g.noCache)
	g.colorOn = g.decideColor(cmd.Flags().Changed("color"))
	if g.colorOn {
		enableVirtualTerminal()
	}
	return nil
}

func (g *globals) decideColor(colorSet bool) bool {
	if g.output != "table" || g.noColor {
		return false
	}
	if colorSet && g.color != "auto" {
		return g.color == "always"
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := g.stdout.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// failOn returns the flag value, else the config value when it is valid for
// this command, else the default.
func (g *globals) failOn(cmd *cobra.Command, flag, def string, allowed []string) (string, error) {
	v := def
	switch {
	case cmd.Flags().Changed("fail-on"):
		v = flag
	case g.cfg.FailOn != "" && slices.Contains(allowed, g.cfg.FailOn):
		v = g.cfg.FailOn
	}
	if !slices.Contains(allowed, v) {
		return "", usageErr("--fail-on must be one of %s", strings.Join(allowed, ", "))
	}
	return v, nil
}

func (g *globals) writer() *report.Writer {
	return &report.Writer{
		Out:     g.stdout,
		Format:  g.output,
		Paint:   report.Painter{On: g.colorOn},
		BaseDir: g.cwd,
		Version: g.info.Version,
	}
}

func (g *globals) logf(prefix string) func(string, ...interface{}) {
	if !g.verbose {
		return nil
	}
	return func(format string, args ...interface{}) {
		fmt.Fprintf(g.stderr, "["+prefix+"] "+format+"\n", args...)
	}
}
