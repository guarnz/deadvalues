package report

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/guarnz/deadvalues/internal/values"
)

var Formats = []string{"table", "json", "markdown", "sarif", "github"}

type Writer struct {
	Out     io.Writer
	Format  string
	Paint   Painter
	Alive   bool
	Details bool
	BaseDir string
	Version string
}

// relativize rewrites file paths relative to BaseDir so every format, JSON
// included, shows the same portable paths.
func (w *Writer) relativize(sources []string, keyOrigins ...[]values.Origin) {
	for i, s := range sources {
		if filepath.IsAbs(s) {
			sources[i] = w.rel(s)
		}
	}
	for _, origins := range keyOrigins {
		for i, o := range origins {
			if o.IsFile {
				origins[i].File = w.rel(o.File)
			}
		}
	}
}

func (w *Writer) Checks(cs []*Check, scan bool) error {
	for _, c := range cs {
		var origins [][]values.Origin
		for i := range c.Keys {
			origins = append(origins, c.Keys[i].Origins)
		}
		w.relativize(c.Values, origins...)
		c.AppFile = w.rel(c.AppFile)
	}
	switch w.Format {
	case "json":
		if !scan && len(cs) == 1 {
			return w.json(cs[0])
		}
		return w.json(cs)
	case "markdown":
		w.checksMarkdown(cs, scan)
	case "sarif":
		return w.sarif(w.checkAnnotations(cs))
	case "github":
		w.github(w.checkAnnotations(cs))
	default:
		w.checksTable(cs, scan)
	}
	return nil
}

func (w *Writer) Diffs(ds []*Diff) error {
	for _, d := range ds {
		var origins [][]values.Origin
		for i := range d.Changes {
			origins = append(origins, d.Changes[i].Origins)
		}
		w.relativize(d.Values, origins...)
		d.AppFile = w.rel(d.AppFile)
	}
	switch w.Format {
	case "json":
		if len(ds) == 1 {
			return w.json(ds[0])
		}
		return w.json(ds)
	case "markdown":
		w.diffsMarkdown(ds)
	case "sarif":
		return w.sarif(w.diffAnnotations(ds))
	case "github":
		w.github(w.diffAnnotations(ds))
	default:
		w.diffsTable(ds)
	}
	return nil
}

func (w *Writer) Explains(es []*Explain) error {
	switch w.Format {
	case "json":
		if len(es) == 1 {
			return w.json(es[0])
		}
		return w.json(es)
	case "markdown":
		for _, e := range es {
			fmt.Fprintf(w.Out, "### `%s` — `%s` = `%s`\n\n", e.App, e.Key, short(e.Value))
			if e.Error != "" {
				fmt.Fprintf(w.Out, "> **Error:** %s\n\n", e.Error)
				continue
			}
			if len(e.Changes) == 0 {
				fmt.Fprint(w.Out, "No effect on the rendered output.\n\n")
				continue
			}
			fmt.Fprint(w.Out, "| Object | Field | With the key | Without it |\n|---|---|---|---|\n")
			for _, c := range e.Changes {
				fmt.Fprintf(w.Out, "| `%s` | `%s` | %s | %s |\n", c.Object, c.Field, mdCode(c.Before), mdCode(c.After))
			}
			fmt.Fprintln(w.Out)
		}
	default:
		p := w.Paint
		for i, e := range es {
			if i > 0 {
				fmt.Fprintln(w.Out)
			}
			if e.Error != "" {
				fmt.Fprintf(w.Out, "%s  %s %s\n", p.Bold(e.App), p.Red("error:"), e.Error)
				continue
			}
			fmt.Fprintf(w.Out, "%s  %s\n", p.Bold(e.App), e.Chart)
			if len(e.Changes) == 0 {
				fmt.Fprintf(w.Out, "%s = %s has no effect on the rendered output\n", p.Bold(e.Key), short(e.Value))
				continue
			}
			fmt.Fprintf(w.Out, "%s = %s affects:\n", p.Bold(e.Key), short(e.Value))
			obj := ""
			for _, c := range e.Changes {
				if c.Object != obj {
					obj = c.Object
					fmt.Fprintf(w.Out, "  %s\n", p.Cyan(obj))
				}
				fmt.Fprintf(w.Out, "    %-45s %s  %s\n", c.Field, orAbsent(c.Before), p.Dim("(without it: "+orAbsent(c.After)+")"))
			}
		}
	}
	return nil
}

func (w *Writer) json(v interface{}) error {
	enc := json.NewEncoder(w.Out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Rel returns p relative to BaseDir with forward slashes, or p unchanged
// when no relative path exists (e.g. another drive on Windows).
func (w *Writer) Rel(p string) string { return w.rel(p) }

func (w *Writer) rel(p string) string {
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	if r, err := filepath.Rel(w.BaseDir, p); err == nil {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(p)
}

func (w *Writer) location(origins []values.Origin) string {
	var locs []string
	for _, o := range origins {
		l := o.File
		if o.IsFile {
			l = w.rel(o.File)
		}
		if o.Line > 0 {
			l += ":" + strconv.Itoa(o.Line)
		}
		locs = append(locs, l)
	}
	return strings.Join(locs, ", ")
}

func (w *Writer) rels(paths []string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = w.rel(p)
	}
	return strings.Join(out, ", ")
}

func orAbsent(s string) string {
	if s == "" {
		return "<absent>"
	}
	return s
}
