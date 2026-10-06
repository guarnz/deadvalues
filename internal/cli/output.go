package cli

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/guarnz/deadvalues/internal/report"
)

// outputFile is one --output-file: a format written to a path. prefixed is
// true for FORMAT=FILE, which keeps the -o output on stdout.
type outputFile struct {
	format   string
	path     string
	prefixed bool
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

// emit writes a report to stdout and to every --output-file. A plain
// --output-file PATH takes the place of stdout; FORMAT=FILE entries are
// written in addition to it. Files are UTF-8 and never colored.
func (g *globals) emit(write func(*report.Writer) error) error {
	toStdout := true
	for _, o := range g.outputs {
		if !o.prefixed {
			toStdout = false
		}
	}
	if toStdout {
		if err := write(g.writer()); err != nil {
			return err
		}
	}
	for _, o := range g.outputs {
		var buf bytes.Buffer
		w := g.writer()
		w.Out, w.Format, w.Paint = &buf, o.format, report.Painter{}
		if err := write(w); err != nil {
			return err
		}
		if err := os.WriteFile(o.path, buf.Bytes(), 0o644); err != nil {
			return &exitError{code: 2, err: err}
		}
		fmt.Fprintf(g.stderr, "report written to %s\n", o.path)
	}
	return nil
}

// noOutputFiles rejects --output-file on commands that produce no report.
func (g *globals) noOutputFiles(command string) error {
	if len(g.outputs) > 0 {
		return usageErr("%s does not produce a report; --output-file is not supported", command)
	}
	return nil
}
