package report

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

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
	skipped := map[string]int{}
	for _, c := range cs {
		if c.Error != "" {
			failed++
			continue
		}
		if c.Skipped != "" {
			skipped[c.SkipLabel()]++
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
	for _, label := range []string{"not analyzed", "not applied", "not supported yet"} {
		if skipped[label] > 0 {
			parts = append(parts, p.Yellow(fmt.Sprintf("%d %s", skipped[label], label)))
		}
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
	if c.Skipped != "" {
		fmt.Fprintf(w.Out, "%s  %s %s\n", p.Bold(c.App), p.Yellow(c.SkipLabel()+":"), c.Skipped)
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
	for _, k := range bySeverity(c.Keys) {
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

var severity = map[string]int{"DEAD": 0, "SHARED": 1, "CONDITIONAL": 2, "REDUNDANT": 3, "ERROR": 4, "GROUP": 5, "ALIVE": 6}

// bySeverity returns the keys with the most severe status first, keeping the
// key order within each status.
func bySeverity(keys []Key) []Key {
	out := append([]Key(nil), keys...)
	sort.SliceStable(out, func(i, j int) bool { return severity[out[i].Label()] < severity[out[j].Label()] })
	return out
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
		if c.Skipped != "" {
			fmt.Fprintf(w.Out, "%-*s  %s %s\n", appW, c.App, p.Yellow(c.SkipLabel()+":"), c.Skipped)
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

func duration(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + "s"
}
