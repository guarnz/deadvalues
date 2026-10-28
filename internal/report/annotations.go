package report

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/guarnz/deadvalues/internal/values"
)

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
		if c.Skipped != "" {
			f, _ := w.firstFile(nil, c.AppFile)
			out = append(out, annotation{rule: "not-analyzed", level: "note", title: "deadvalues: " + c.App, message: "not analyzed: " + c.Skipped, file: f})
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
	"dead":         "Value has no effect on the rendered chart",
	"conditional":  "Value only has an effect when a disabled feature is enabled",
	"redundant":    "Value equals the chart default",
	"breaking":     "Value stops having an effect in the new chart version",
	"pinned":       "Value now pins a default the new chart version changed",
	"changed":      "A default you do not set changed and alters the manifests",
	"fixed":        "Value starts having an effect in the new chart version",
	"error":        "deadvalues could not analyze the target",
	"not-analyzed": "deadvalues found the target but cannot analyze it yet",
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

func ghData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func ghProp(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}
