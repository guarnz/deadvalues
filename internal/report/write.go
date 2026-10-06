package report

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

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

func (w *Writer) checksTable(cs []*Check, scan bool) {
	if scan {
		w.scanSummary(cs)
		if w.Details {
			for _, c := range cs {
				fmt.Fprintln(w.Out)
				w.checkTable(c)
			}
		}
		fmt.Fprintln(w.Out)
		fmt.Fprintln(w.Out, w.totals(cs))
		return
	}
	for i, c := range cs {
		if i > 0 {
			fmt.Fprintln(w.Out)
		}
		w.checkTable(c)
	}
}

// totals is the one-line summary of a scan across every app.
func (w *Writer) totals(cs []*Check) string {
	p := w.Paint
	var withDead, dead, cond, redundant, failed int
	for _, c := range cs {
		if c.Error != "" {
			failed++
			continue
		}
		if c.Counts["DEAD"] > 0 {
			withDead++
		}
		dead += c.Counts["DEAD"]
		cond += c.Counts["CONDITIONAL"]
		redundant += c.Counts["REDUNDANT"]
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, one)
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	deadText := fmt.Sprintf("%d dead", dead)
	if dead > 0 {
		deadText = p.Red(deadText)
	}
	parts := []string{
		plural(len(cs), "app", "apps"),
		fmt.Sprintf("%d with dead keys", withDead),
		deadText,
		fmt.Sprintf("%d conditional", cond),
		fmt.Sprintf("%d redundant", redundant),
	}
	if failed > 0 {
		parts = append(parts, p.Red(plural(failed, "error", "errors")))
	}
	return strings.Join(parts, " · ")
}

func (w *Writer) checkTable(c *Check) {
	p := w.Paint
	if c.Error != "" {
		fmt.Fprintf(w.Out, "%s  %s %s\n", p.Bold(c.App), p.Red("error:"), c.Error)
		return
	}
	if len(c.Values) == 0 {
		fmt.Fprintf(w.Out, "%s  %s\n", p.Bold(c.App), c.Chart)
		fmt.Fprintln(w.Out, "  "+p.Dim("no values set, nothing to check"))
		return
	}
	fmt.Fprintf(w.Out, "%s  %s  ←  %s\n", p.Bold(c.App), c.Chart, w.rels(c.Values))
	stats := fmt.Sprintf("%d renders in %s", c.Renders, duration(c.Millis))
	if c.Noise > 0 {
		stats += fmt.Sprintf(", %d random fields ignored", c.Noise)
	}
	fmt.Fprintln(w.Out, p.Dim(stats))
	for _, n := range c.Notes {
		fmt.Fprintln(w.Out, p.Dim("note: "+n))
	}
	fmt.Fprintln(w.Out)

	var shown []Key
	width := 0
	for _, k := range c.Keys {
		if k.Ignored || k.UsedBy != "" || (k.status.Effective() && !w.Alive) {
			continue
		}
		shown = append(shown, k)
		if n := utf8.RuneCountInString(k.Key); n > width {
			width = n
		}
	}
	if width > 50 {
		width = 50
	}
	multi := len(c.Values) > 1
	for _, k := range shown {
		line := fmt.Sprintf("  %s %-*s  %s", p.Status(k.Label(), 11), width, k.Key, k.Detail)
		if multi && !k.status.Effective() {
			line += "  " + p.Dim(w.location(k.Origins))
		}
		fmt.Fprintln(w.Out, line)
	}
	if len(shown) > 0 {
		fmt.Fprintln(w.Out)
	}
	fmt.Fprintln(w.Out, "  "+w.counts(c))
}

func (w *Writer) counts(c *Check) string {
	p := w.Paint
	dead := fmt.Sprintf("%d dead", c.Counts["DEAD"])
	if c.Counts["DEAD"] > 0 {
		dead = p.Red(dead)
	}
	parts := []string{
		dead,
		fmt.Sprintf("%d conditional", c.Counts["CONDITIONAL"]),
		fmt.Sprintf("%d redundant", c.Counts["REDUNDANT"]),
		fmt.Sprintf("%d alive", c.Counts["ALIVE"]),
	}
	if n := c.Counts["GROUP"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d group", n))
	}
	if n := c.Counts["ERROR"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d required", n))
	}
	if c.Shared > 0 {
		parts = append(parts, fmt.Sprintf("%d used by other apps", c.Shared))
	}
	if c.Unproven > 0 {
		parts = append(parts, fmt.Sprintf("%d in shared files (run scan)", c.Unproven))
	}
	if c.Ignored > 0 {
		parts = append(parts, fmt.Sprintf("%d ignored", c.Ignored))
	}
	return strings.Join(parts, ", ")
}

func (w *Writer) scanSummary(cs []*Check) {
	p := w.Paint
	appW, chartW := len("APP"), len("CHART")
	for _, c := range cs {
		appW = max(appW, utf8.RuneCountInString(c.App))
		chartW = max(chartW, utf8.RuneCountInString(c.Chart))
	}
	fmt.Fprintf(w.Out, "%-*s  %-*s  %5s  %5s  %9s  %5s\n", appW, "APP", chartW, "CHART", "DEAD", "COND", "REDUNDANT", "ALIVE")
	for _, c := range cs {
		if c.Error != "" {
			fmt.Fprintf(w.Out, "%-*s  %s %s\n", appW, c.App, p.Red("error:"), c.Error)
			continue
		}
		if len(c.Values) == 0 {
			fmt.Fprintf(w.Out, "%-*s  %-*s  %5s  %5s  %9s  %5s\n", appW, c.App, chartW, c.Chart, "-", "-", "-", "-")
			continue
		}
		dead := fmt.Sprintf("%5d", c.Counts["DEAD"])
		if c.Counts["DEAD"] > 0 {
			dead = p.Red(dead)
		}
		fmt.Fprintf(w.Out, "%-*s  %-*s  %s  %5d  %9d  %5d\n", appW, c.App, chartW, c.Chart, dead,
			c.Counts["CONDITIONAL"], c.Counts["REDUNDANT"], c.Counts["ALIVE"]+c.Counts["GROUP"]+c.Counts["ERROR"])
	}
}

func (w *Writer) checksMarkdown(cs []*Check, scan bool) {
	if scan {
		fmt.Fprint(w.Out, "| App | Chart | Dead | Conditional | Redundant | Alive |\n|---|---|---:|---:|---:|---:|\n")
		for _, c := range cs {
			if c.Error != "" {
				fmt.Fprintf(w.Out, "| `%s` | **Error:** %s | | | | |\n", c.App, mdEscape(c.Error))
				continue
			}
			if len(c.Values) == 0 {
				fmt.Fprintf(w.Out, "| `%s` | %s | - | - | - | - |\n", c.App, c.Chart)
				continue
			}
			fmt.Fprintf(w.Out, "| `%s` | %s | %d | %d | %d | %d |\n", c.App, c.Chart, c.Counts["DEAD"], c.Counts["CONDITIONAL"],
				c.Counts["REDUNDANT"], c.Counts["ALIVE"]+c.Counts["GROUP"]+c.Counts["ERROR"])
		}
		saved := w.Paint
		w.Paint = Painter{}
		fmt.Fprintf(w.Out, "\n**%s**\n\n", w.totals(cs))
		w.Paint = saved
		if !w.Details {
			return
		}
	}
	for _, c := range cs {
		fmt.Fprintf(w.Out, "### `%s` — %s\n\n", c.App, c.Chart)
		if c.Error != "" {
			fmt.Fprintf(w.Out, "> **Error:** %s\n\n", mdEscape(c.Error))
			continue
		}
		if len(c.Values) == 0 {
			fmt.Fprint(w.Out, "_No values set, nothing to check._\n\n")
			continue
		}
		for _, n := range c.Notes {
			fmt.Fprintf(w.Out, "> **Note:** %s\n\n", mdEscape(n))
		}
		var rows []Key
		for _, k := range c.Keys {
			if !k.Ignored && k.UsedBy == "" && (!k.status.Effective() || w.Alive) {
				rows = append(rows, k)
			}
		}
		if len(rows) == 0 {
			fmt.Fprint(w.Out, "No dead or redundant keys.\n\n")
			continue
		}
		fmt.Fprint(w.Out, "| Status | Key | Detail | Source |\n|---|---|---|---|\n")
		for _, k := range rows {
			fmt.Fprintf(w.Out, "| %s | `%s` | %s | %s |\n", mdStatus(k.Label()), k.Key, mdEscape(k.Detail), mdEscape(w.location(k.Origins)))
		}
		fmt.Fprintf(w.Out, "\n%s\n\n", w.plainCounts(c))
	}
}

func (w *Writer) plainCounts(c *Check) string {
	saved := w.Paint
	w.Paint = Painter{}
	defer func() { w.Paint = saved }()
	return "**" + w.counts(c) + "**"
}

func (w *Writer) diffsTable(ds []*Diff) {
	p := w.Paint
	for i, d := range ds {
		if i > 0 {
			fmt.Fprintln(w.Out)
		}
		if d.Error != "" {
			fmt.Fprintf(w.Out, "%s  %s %s\n", p.Bold(d.App), p.Red("error:"), d.Error)
			continue
		}
		fmt.Fprintf(w.Out, "%s  %s %s → %s\n", p.Bold(d.App), d.Chart, d.From, d.To)
		fmt.Fprintln(w.Out, p.Dim(fmt.Sprintf("%d renders in %s", d.Renders, duration(d.Millis))))
		fmt.Fprintln(w.Out)
		width := 0
		for _, c := range d.Changes {
			if !c.Ignored {
				width = max(width, utf8.RuneCountInString(c.Key))
			}
		}
		width = min(width, 50)
		shown := 0
		for _, c := range d.Changes {
			if c.Ignored {
				continue
			}
			shown++
			fmt.Fprintf(w.Out, "  %s %-*s  %s\n", p.Status(c.Kind, 9), width, c.Key, c.Detail)
		}
		if shown == 0 {
			fmt.Fprintln(w.Out, "  "+p.Green("no key changed behavior"))
			continue
		}
		fmt.Fprintf(w.Out, "\n  %d breaking, %d pinned, %d changed, %d fixed\n",
			d.Counts["BREAKING"], d.Counts["PINNED"], d.Counts["CHANGED"], d.Counts["FIXED"])
	}
}

func (w *Writer) diffsMarkdown(ds []*Diff) {
	for _, d := range ds {
		fmt.Fprintf(w.Out, "### `%s` — %s %s → %s\n\n", d.App, d.Chart, d.From, d.To)
		if d.Error != "" {
			fmt.Fprintf(w.Out, "> **Error:** %s\n\n", mdEscape(d.Error))
			continue
		}
		var rows []Change
		for _, c := range d.Changes {
			if !c.Ignored {
				rows = append(rows, c)
			}
		}
		if len(rows) == 0 {
			fmt.Fprint(w.Out, "No key changed behavior.\n\n")
			continue
		}
		fmt.Fprint(w.Out, "| Change | Key | Detail | Source |\n|---|---|---|---|\n")
		for _, c := range rows {
			fmt.Fprintf(w.Out, "| %s | `%s` | %s | %s |\n", mdStatus(c.Kind), c.Key, mdEscape(c.Detail), mdEscape(w.location(c.Origins)))
		}
		fmt.Fprintf(w.Out, "\n**%d breaking, %d pinned, %d changed, %d fixed**\n\n",
			d.Counts["BREAKING"], d.Counts["PINNED"], d.Counts["CHANGED"], d.Counts["FIXED"])
	}
}

type annotation struct {
	rule    string
	level   string
	title   string
	message string
	file    string
	line    int
}

func (w *Writer) firstFile(origins []values.Origin, fallback string) (string, int) {
	for _, o := range origins {
		if o.IsFile {
			return w.rel(o.File), o.Line
		}
	}
	if fallback != "" {
		return w.rel(fallback), 0
	}
	return "", 0
}

func (w *Writer) checkAnnotations(cs []*Check) []annotation {
	var out []annotation
	for _, c := range cs {
		if c.Error != "" {
			f, _ := w.firstFile(nil, c.AppFile)
			out = append(out, annotation{rule: "error", level: "error", title: "deadvalues: " + c.App, message: c.Error, file: f})
			continue
		}
		for _, k := range c.Keys {
			if !k.Reportable() {
				continue
			}
			level := "warning"
			if k.Status == "REDUNDANT" {
				level = "note"
			}
			f, line := w.firstFile(k.Origins, c.AppFile)
			out = append(out, annotation{
				rule:    strings.ToLower(k.Status),
				level:   level,
				title:   "deadvalues: " + k.Status,
				message: fmt.Sprintf("%s: %s %s", c.App, k.Key, k.Detail),
				file:    f,
				line:    line,
			})
		}
	}
	return out
}

func (w *Writer) diffAnnotations(ds []*Diff) []annotation {
	var out []annotation
	for _, d := range ds {
		if d.Error != "" {
			f, _ := w.firstFile(nil, d.AppFile)
			out = append(out, annotation{rule: "error", level: "error", title: "deadvalues: " + d.App, message: d.Error, file: f})
			continue
		}
		for _, c := range d.Changes {
			if c.Ignored {
				continue
			}
			level := "warning"
			switch c.Kind {
			case "BREAKING":
				level = "error"
			case "FIXED":
				level = "note"
			}
			f, line := w.firstFile(c.Origins, d.AppFile)
			out = append(out, annotation{
				rule:    strings.ToLower(c.Kind),
				level:   level,
				title:   "deadvalues: " + c.Kind,
				message: fmt.Sprintf("%s (%s %s → %s): %s %s", d.App, d.Chart, d.From, d.To, c.Key, c.Detail),
				file:    f,
				line:    line,
			})
		}
	}
	return out
}

func (w *Writer) github(as []annotation) {
	for _, a := range as {
		cmd := map[string]string{"error": "error", "warning": "warning", "note": "notice"}[a.level]
		var props []string
		if a.file != "" {
			props = append(props, "file="+ghProp(a.file))
			if a.line > 0 {
				props = append(props, "line="+strconv.Itoa(a.line))
			}
		}
		props = append(props, "title="+ghProp(a.title))
		fmt.Fprintf(w.Out, "::%s %s::%s\n", cmd, strings.Join(props, ","), ghData(a.message))
	}
}

var ruleText = map[string]string{
	"dead":        "Value has no effect on the rendered chart",
	"conditional": "Value only has an effect when a disabled feature is enabled",
	"redundant":   "Value equals the chart default",
	"breaking":    "Value stops having an effect in the new chart version",
	"pinned":      "Value now pins a default the new chart version changed",
	"changed":     "A default you do not set changed and alters the manifests",
	"fixed":       "Value starts having an effect in the new chart version",
	"error":       "deadvalues could not analyze the target",
}

func (w *Writer) sarif(as []annotation) error {
	type m = map[string]interface{}
	ruleSet := map[string]bool{}
	var rules []m
	var results []m
	for _, a := range as {
		if !ruleSet[a.rule] {
			ruleSet[a.rule] = true
			rules = append(rules, m{"id": a.rule, "shortDescription": m{"text": ruleText[a.rule]}})
		}
		r := m{"ruleId": a.rule, "level": a.level, "message": m{"text": a.message}}
		if a.file != "" {
			loc := m{"artifactLocation": m{"uri": a.file}}
			if a.line > 0 {
				loc["region"] = m{"startLine": a.line}
			}
			r["locations"] = []m{{"physicalLocation": loc}}
		}
		results = append(results, r)
	}
	if rules == nil {
		rules = []m{}
	}
	if results == nil {
		results = []m{}
	}
	return w.json(m{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []m{{
			"tool": m{"driver": m{
				"name":           "deadvalues",
				"informationUri": "https://github.com/guarnz/deadvalues",
				"version":        w.Version,
				"rules":          rules,
			}},
			"results": results,
		}},
	})
}

func duration(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + "s"
}

func ghData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func ghProp(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}

func mdCode(s string) string {
	if s == "" {
		return "_absent_"
	}
	return "`" + mdEscape(s) + "`"
}

func orAbsent(s string) string {
	if s == "" {
		return "<absent>"
	}
	return s
}
