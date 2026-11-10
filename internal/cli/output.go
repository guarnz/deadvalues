package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/guarnz/deadvalues/internal/report"
)

// outputFile is one --output-file: a format written to a path. prefixed is
// true for FORMAT=FILE, which keeps the -o output on stdout.
type outputFile struct {
	format   string
	path     string
	prefixed bool
	mkdir    bool
}

func parseOutputFiles(values []string, defaultFormat string) []outputFile {
	var out []outputFile
	for _, v := range values {
		if format, path, ok := strings.Cut(v, "="); ok && slices.Contains(report.Formats, format) {
			out = append(out, outputFile{format: format, path: path, prefixed: true})
			continue
		}
		out = append(out, outputFile{format: defaultFormat, path: v})
	}
	return out
}

var fileExt = map[string]string{"json": ".json", "markdown": ".md", "sarif": ".sarif"}

// reportFiles places the -o formats that are files, or every file format the command writes when
// -o names none, at path: a directory, a base name, or one file. An empty path is the current one.
func reportFiles(command, path string, formats []string, now time.Time) ([]outputFile, error) {
	var files []string
	for _, f := range formats {
		if fileExt[f] != "" && !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	byExt := ""
	for f, ext := range fileExt {
		if strings.EqualFold(filepath.Ext(path), ext) {
			byExt = f
		}
	}
	base := command + "-" + now.Format("2006-01-02")
	switch {
	case path == "":
	case strings.HasSuffix(path, "/") || strings.HasSuffix(path, string(filepath.Separator)) || isDir(path):
		base = filepath.Join(path, base)
	case byExt != "":
		if len(files) == 0 {
			files = []string{byExt}
		}
		if len(files) > 1 || files[0] != byExt {
			return nil, usageErr("--report-path %s is one %s file, but -o asks for %s", path, byExt, strings.Join(files, ", "))
		}
		return []outputFile{{format: byExt, path: path, prefixed: true, mkdir: true}}, nil
	default:
		base = path
	}
	if len(files) == 0 {
		files = []string{"json", "markdown", "sarif"}
		if command == "explain" {
			files = files[:2]
		}
	}
	var out []outputFile
	for _, f := range files {
		out = append(out, outputFile{format: f, path: base + fileExt[f], prefixed: true, mkdir: true})
	}
	return out, nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// emit writes a report to stdout and every --output-file (UTF-8, never colored): a
// plain PATH replaces stdout, and FORMAT=FILE entries are written in addition to it.
func (g *globals) emit(write func(*report.Writer) error) error {
	toStdout := true
	for _, o := range g.outputs {
		if !o.prefixed {
			toStdout = false
		}
	}
	generated := time.Now()
	if toStdout {
		w := g.writer()
		w.Generated = generated
		if err := write(w); err != nil {
			return err
		}
	}
	for _, o := range g.outputs {
		var buf bytes.Buffer
		w := g.writer()
		w.Out, w.Format, w.Paint, w.Generated = &buf, o.format, report.Painter{}, generated
		if err := write(w); err != nil {
			return err
		}
		if dir := filepath.Dir(o.path); o.mkdir && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return &exitError{code: 2, err: err}
			}
		}
		if err := os.WriteFile(o.path, buf.Bytes(), 0o644); err != nil {
			return &exitError{code: 2, err: err}
		}
		fmt.Fprintf(g.stderr, "report written to %s\n", o.path)
	}
	return nil
}

// noOutputFiles rejects report files on commands that produce no report.
func (g *globals) noOutputFiles(command string) error {
	if len(g.outputs) > 0 {
		return usageErr("%s does not produce a report; --report, --report-path and --output-file are not supported", command)
	}
	return nil
}

// outputFormats sets the format printed on stdout and the report files from -o, --report and
// --report-path; several formats in -o need --report, which writes those that are files.
func (g *globals) outputFormats(command string, now time.Time) error {
	var screen []string
	for _, f := range g.formats {
		if !slices.Contains(report.Formats, f) {
			return usageErr("--output must be one of %s", strings.Join(report.Formats, ", "))
		}
		if fileExt[f] == "" && !slices.Contains(screen, f) {
			screen = append(screen, f)
		}
	}
	if len(g.formats) == 0 {
		return usageErr("--output must be one of %s", strings.Join(report.Formats, ", "))
	}
	if !g.report && g.reportPath == "" {
		if len(g.formats) > 1 {
			return usageErr("-o takes several formats only with --report or --report-path, which write json, markdown and sarif to files")
		}
		g.output = g.formats[0]
		g.outputs = parseOutputFiles(g.outputFiles, g.output)
		return nil
	}
	if len(screen) > 1 {
		return usageErr("-o can print only one of %s on screen", strings.Join(screen, " or "))
	}
	g.output = "table"
	if len(screen) == 1 {
		g.output = screen[0]
	}
	files, err := reportFiles(command, g.reportPath, g.formats, now)
	if err != nil {
		return err
	}
	g.outputs = append(parseOutputFiles(g.outputFiles, g.output), files...)
	return nil
}
