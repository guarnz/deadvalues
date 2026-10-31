package report

import (
	"fmt"
	"strings"
)

func (w *Writer) checksMarkdown(cs []*Check, scan bool) {
	if scan {
		fmt.Fprint(w.Out, "| App | Chart | Dead | Conditional | Redundant | Alive |\n|---|---|---:|---:|---:|---:|\n")
		for _, c := range cs {
			if c.Error != "" {
				fmt.Fprintf(w.Out, "| `%s` | **Error:** %s | | | | |\n", c.App, mdEscape(c.Error))
				continue
			}
			if c.Skipped != "" {
				fmt.Fprintf(w.Out, "| `%s` | _%s:_ %s | | | | |\n", c.App, sentence(c.SkipLabel()), mdEscape(c.Skipped))
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
		if c.Skipped != "" {
			fmt.Fprintf(w.Out, "> **%s:** %s\n\n", sentence(c.SkipLabel()), mdEscape(c.Skipped))
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
		for _, k := range bySeverity(c.Keys) {
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

// sentence starts a lower-case label with a capital letter.
func sentence(s string) string {
	return strings.ToUpper(s[:1]) + s[1:]
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
