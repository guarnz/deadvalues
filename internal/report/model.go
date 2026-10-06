// Package report turns probe and diff results into user-facing reports and
// writes them as table, json, markdown, sarif or GitHub annotations.
package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/guarnz/deadvalues/internal/compare"
	"github.com/guarnz/deadvalues/internal/diff"
	"github.com/guarnz/deadvalues/internal/probe"
	"github.com/guarnz/deadvalues/internal/values"
)

type Key struct {
	Key     string          `json:"key"`
	Path    []string        `json:"path"`
	Status  string          `json:"status"`
	Value   interface{}     `json:"value"`
	Default interface{}     `json:"default,omitempty"`
	Detail  string          `json:"detail,omitempty"`
	Origins []values.Origin `json:"origins,omitempty"`
	Ignored bool            `json:"ignored,omitempty"`
	UsedBy  string          `json:"usedBy,omitempty"`
	// SharedWith lists other Applications loading the same values file when
	// only this app was analyzed: the key may be used by them.
	SharedWith []string `json:"sharedWith,omitempty"`

	status probe.Status
}

// Label is the status shown to the user.
func (k Key) Label() string {
	if len(k.SharedWith) > 0 {
		return "SHARED"
	}
	return k.Status
}

type Check struct {
	App      string         `json:"app"`
	AppFile  string         `json:"appFile,omitempty"`
	Chart    string         `json:"chart"`
	Values   []string       `json:"values"`
	Renders  int64          `json:"renders"`
	Millis   int64          `json:"durationMs"`
	Noise    int            `json:"noisyFields"`
	Keys     []Key          `json:"keys"`
	Counts   map[string]int `json:"counts"`
	Ignored  int            `json:"ignored"`
	Shared   int            `json:"usedByOtherApps"`
	Notes    []string       `json:"notes,omitempty"`
	Unproven int            `json:"sharedUnverified"`
	Error    string         `json:"error,omitempty"`
}

// ResolveShared handles values files loaded by several targets, such as a
// global values file passed to many charts: a key that has no effect in one
// chart but is used by another chart loading the same file is not reported.
func ResolveShared(cs []*Check) {
	users := map[string][]*Check{}
	for _, c := range cs {
		for _, f := range c.Values {
			users[f] = append(users[f], c)
		}
	}
	for _, c := range cs {
		for i := range c.Keys {
			k := &c.Keys[i]
			if k.Ignored || k.status.Effective() {
				continue
			}
			by := usedElsewhere(c, k, users)
			if by == "" {
				continue
			}
			k.UsedBy = by
			c.Counts[k.Status]--
			c.Shared++
		}
	}
}

// MarkShared handles a single-app check: keys with no effect that come only
// from values files other Applications also load cannot be judged from this
// app alone. They are shown as SHARED, pointing at scan, and do not fail.
// users maps a normalized file path to the other Applications loading it.
func MarkShared(c *Check, users map[string][]string, norm func(string) string) {
	for i := range c.Keys {
		k := &c.Keys[i]
		if k.Ignored || k.UsedBy != "" || k.status.Effective() || len(k.Origins) == 0 {
			continue
		}
		var apps []string
		shared := true
		for _, o := range k.Origins {
			names := users[norm(o.File)]
			if !o.IsFile || o.Prefix != nil || len(names) == 0 {
				shared = false
				break
			}
			for _, n := range names {
				if !contains(apps, n) {
					apps = append(apps, n)
				}
			}
		}
		if !shared {
			continue
		}
		k.SharedWith = apps
		k.Detail = short(k.Value) + "  (no effect in this chart; the file is shared with " + listApps(apps) + ", run scan to check them)"
		c.Counts[k.Status]--
		c.Unproven++
	}
}

func listApps(apps []string) string {
	if len(apps) <= 3 {
		return strings.Join(apps, ", ")
	}
	return strings.Join(apps[:3], ", ") + fmt.Sprintf(" and %d more", len(apps)-3)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func usedElsewhere(c *Check, k *Key, users map[string][]*Check) string {
	for _, o := range k.Origins {
		// Values embedded in a manifest (a HelmRelease's spec.values) belong
		// to that release only, even when other releases share the file.
		if !o.IsFile || o.Prefix != nil {
			return ""
		}
		for _, other := range users[o.File] {
			if other == c {
				continue
			}
			if s, ok := other.status(k.Path); ok && s.Effective() {
				return other.App
			}
		}
	}
	return ""
}

func (c *Check) status(path []string) (probe.Status, bool) {
	for i := len(path); i > 0; i-- {
		key := values.Key(path[:i])
		for _, k := range c.Keys {
			if values.Key(k.Path) == key {
				return k.status, true
			}
		}
	}
	return 0, false
}

func NewCheck(app, appFile, chart string, set *values.Set, res *probe.Result, ignored func([]string) bool, took time.Duration) *Check {
	c := &Check{
		App:     app,
		AppFile: appFile,
		Chart:   chart,
		Values:  append([]string(nil), set.Sources...),
		Renders: res.Renders,
		Millis:  took.Milliseconds(),
		Noise:   len(res.Noise),
		Counts:  map[string]int{},
	}
	for _, f := range res.Findings {
		k := Key{
			Key:     f.Key(),
			Path:    f.Path,
			Status:  f.Status.String(),
			Value:   f.Value,
			Detail:  findingDetail(f),
			Origins: append([]values.Origin(nil), set.Origins(f.Path)...),
			Ignored: ignored != nil && ignored(f.Path),
			status:  f.Status,
		}
		if f.HasDefault {
			k.Default = f.Default
		}
		c.Keys = append(c.Keys, k)
		if k.Ignored {
			c.Ignored++
			continue
		}
		c.Counts[k.Status]++
	}
	return c
}

// Restrict keeps only the keys set in one of files (normalized with norm)
// and recomputes the counts. Used when the user asked about specific values
// files, so keys coming from the app's other values files are left out.
func (c *Check) Restrict(files map[string]bool, norm func(string) string) {
	var kept []Key
	for _, k := range c.Keys {
		for _, o := range k.Origins {
			if o.IsFile && files[norm(o.File)] {
				kept = append(kept, k)
				break
			}
		}
	}
	c.Keys = kept
	c.Counts = map[string]int{}
	c.Ignored = 0
	for _, k := range c.Keys {
		if k.Ignored {
			c.Ignored++
			continue
		}
		c.Counts[k.Status]++
	}
	var vals []string
	for _, v := range c.Values {
		if files[norm(v)] {
			vals = append(vals, v)
		}
	}
	c.Values = vals
}

func Failed(app string, err error) *Check {
	return &Check{App: app, Error: err.Error(), Counts: map[string]int{}}
}

func findingDetail(f probe.Finding) string {
	switch f.Status {
	case probe.Redundant:
		if f.Implicit {
			return short(f.Value) + "  (= fallback hardcoded in the template)"
		}
		return "= chart default " + short(f.Default)
	case probe.Dead:
		if !f.HasDefault {
			return short(f.Value) + "  (key not in chart values.yaml)"
		}
		return short(f.Value) + "  (chart default " + short(f.Default) + "; no effect with the current values)"
	case probe.Conditional:
		if len(f.RequiresAPI) > 0 {
			return short(f.Value) + "  (only when the cluster serves " + strings.Join(f.RequiresAPI, " + ") + ")"
		}
		return short(f.Value) + "  (only when " + values.Display(f.Toggle) + "=true)"
	case probe.Group:
		return short(f.Value) + "  (only effective as a group)"
	case probe.Error:
		return "removing it breaks the render: " + f.Err
	default:
		return short(f.Value)
	}
}

// Problem reports whether the key is at or above the failOn threshold.
func (k Key) Problem(failOn string) bool {
	if k.Ignored || k.UsedBy != "" || len(k.SharedWith) > 0 {
		return false
	}
	switch failOn {
	case "dead":
		return k.status == probe.Dead
	case "conditional":
		return k.status == probe.Dead || k.status == probe.Conditional
	case "redundant":
		return k.status == probe.Dead || k.status == probe.Conditional || k.status == probe.Redundant
	default:
		return false
	}
}

func (k Key) Reportable() bool {
	return !k.Ignored && k.UsedBy == "" && len(k.SharedWith) == 0 && !k.status.Effective()
}

func (c *Check) Problems(failOn string) int {
	n := 0
	for _, k := range c.Keys {
		if k.Problem(failOn) {
			n++
		}
	}
	return n
}

type Change struct {
	Key        string          `json:"key"`
	Path       []string        `json:"path"`
	Kind       string          `json:"kind"`
	From       string          `json:"from,omitempty"`
	To         string          `json:"to,omitempty"`
	Value      interface{}     `json:"value,omitempty"`
	OldDefault interface{}     `json:"oldDefault,omitempty"`
	NewDefault interface{}     `json:"newDefault,omitempty"`
	Detail     string          `json:"detail"`
	Origins    []values.Origin `json:"origins,omitempty"`
	Ignored    bool            `json:"ignored,omitempty"`

	kind diff.Kind
}

type Diff struct {
	App     string         `json:"app"`
	AppFile string         `json:"appFile,omitempty"`
	Chart   string         `json:"chart"`
	From    string         `json:"from"`
	To      string         `json:"to"`
	Values  []string       `json:"values"`
	Renders int64          `json:"renders"`
	Millis  int64          `json:"durationMs"`
	Changes []Change       `json:"changes"`
	Counts  map[string]int `json:"counts"`
	Error   string         `json:"error,omitempty"`
}

func NewDiff(app, appFile, chart, from, to string, set *values.Set, ts []diff.Transition, renders int64, ignored func([]string) bool, took time.Duration) *Diff {
	d := &Diff{App: app, AppFile: appFile, Chart: chart, From: from, To: to, Values: append([]string(nil), set.Sources...), Renders: renders, Millis: took.Milliseconds(), Counts: map[string]int{}}
	for _, t := range ts {
		c := Change{
			Key:        t.Key(),
			Path:       t.Path,
			Kind:       t.Kind.String(),
			OldDefault: t.OldDefault,
			NewDefault: t.NewDefault,
			Detail:     transitionDetail(t),
			Ignored:    ignored != nil && ignored(t.Path),
			kind:       t.Kind,
		}
		if t.HasValue {
			c.From, c.To, c.Value = t.From.String(), t.To.String(), t.Value
			c.Origins = append([]values.Origin(nil), set.Origins(t.Path)...)
		}
		d.Changes = append(d.Changes, c)
		if !c.Ignored {
			d.Counts[c.Kind]++
		}
	}
	return d
}

func FailedDiff(app string, err error) *Diff {
	return &Diff{App: app, Error: err.Error(), Counts: map[string]int{}}
}

func transitionDetail(t diff.Transition) string {
	switch t.Kind {
	case diff.Breaking:
		return t.From.String() + " → " + t.To.String()
	case diff.Pinned:
		return fmt.Sprintf("%s → %s (default %s → %s; your value now pins the old behavior)", t.From, t.To, short(t.OldDefault), short(t.NewDefault))
	case diff.Changed:
		return fmt.Sprintf("default %s → %s (you do not set it; the manifests change)", short(t.OldDefault), short(t.NewDefault))
	default:
		return t.From.String() + " → " + t.To.String()
	}
}

// Problem reports whether the change is at or above the failOn threshold.
func (c Change) Problem(failOn string) bool {
	if c.Ignored {
		return false
	}
	switch failOn {
	case "breaking":
		return c.kind == diff.Breaking
	case "pinned":
		return c.kind == diff.Breaking || c.kind == diff.Pinned
	case "changed":
		return c.kind == diff.Breaking || c.kind == diff.Pinned || c.kind == diff.Changed
	default:
		return false
	}
}

func (d *Diff) Problems(failOn string) int {
	n := 0
	for _, c := range d.Changes {
		if c.Problem(failOn) {
			n++
		}
	}
	return n
}

type Explain struct {
	App     string           `json:"app"`
	Chart   string           `json:"chart"`
	Key     string           `json:"key"`
	Value   interface{}      `json:"value"`
	Changes []compare.Change `json:"changes"`
	Error   string           `json:"error,omitempty"`
}

func short(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if len(b) > 60 {
		return string(b[:57]) + "..."
	}
	return string(b)
}
