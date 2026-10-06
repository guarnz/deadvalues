// Package probe finds which user-set values change a chart's rendered
// output: it removes each subtree, re-renders and compares with a baseline.
package probe

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/guarnz/deadvalues/internal/compare"
	"github.com/guarnz/deadvalues/internal/values"
)

type Status int

const (
	Alive Status = iota
	Group
	Error
	Redundant
	Conditional
	Dead
)

func (s Status) String() string {
	return [...]string{"ALIVE", "GROUP", "ERROR", "REDUNDANT", "CONDITIONAL", "DEAD"}[s]
}

// Effective reports whether the key has an effect on the rendered output.
func (s Status) Effective() bool { return s == Alive || s == Group || s == Error }

type Finding struct {
	Path        []string
	Status      Status
	Value       interface{}
	Default     interface{}
	HasDefault  bool
	Implicit    bool
	Toggle      []string
	RequiresAPI []string
	Err         string
}

func (f Finding) Key() string { return values.Display(f.Path) }

type RenderFunc func(map[string]interface{}) (compare.Snapshot, error)

type Options struct {
	Render   RenderFunc
	User     map[string]interface{}
	Defaults map[string]interface{}
	Workers  int
	Logf     func(format string, args ...interface{})

	// MissingAPIs are API versions the chart checks with
	// .Capabilities.APIVersions.Has that the render capabilities lack.
	// RenderWithAPIs renders as if the cluster also served them. When both
	// are set, keys that only matter with one of those APIs are reported as
	// CONDITIONAL instead of DEAD.
	MissingAPIs    []string
	RenderWithAPIs func(vals map[string]interface{}, apis []string) (compare.Snapshot, error)
}

type Result struct {
	Findings []Finding
	Renders  int64
	Base     compare.Snapshot
	Noise    map[string]bool
}

// Find returns the finding for path, or for its closest ancestor when the
// key was classified as part of a larger subtree (GROUP).
func (r *Result) Find(path []string) (Finding, bool) {
	for i := len(path); i > 0; i-- {
		key := values.Key(path[:i])
		for _, f := range r.Findings {
			if values.Key(f.Path) == key {
				return f, true
			}
		}
	}
	return Finding{}, false
}

// variant is an alternative baseline: the user values with a flag turned on,
// or the same values rendered with extra API versions available.
type variant struct {
	once   sync.Once
	ok     bool
	user   map[string]interface{}
	render RenderFunc
	base   compare.Snapshot
	noise  map[string]bool
}

type prober struct {
	Options
	base        compare.Snapshot
	noise       map[string]bool
	sem         chan struct{}
	renders     atomic.Int64
	variants    sync.Map
	userToggles [][]string
	mu          sync.Mutex
	findings    []Finding
}

func Run(o Options) (*Result, error) {
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.Logf == nil {
		o.Logf = func(string, ...interface{}) {}
	}
	p := &prober{Options: o, sem: make(chan struct{}, o.Workers)}

	base, noise, err := p.baseline(o.Render, o.User)
	if err != nil {
		return nil, err
	}
	p.base, p.noise = base, noise
	o.Logf("baseline: %d fields, %d noisy", len(base), len(noise))

	values.Leaves(o.User, nil, func(path []string, v interface{}) {
		if b, ok := v.(bool); ok && !b && path[len(path)-1] == "enabled" {
			p.userToggles = append(p.userToggles, path)
		}
	})
	sort.Slice(p.userToggles, func(i, j int) bool {
		return values.Display(p.userToggles[i]) < values.Display(p.userToggles[j])
	})

	p.children(nil, o.User)

	sort.Slice(p.findings, func(i, j int) bool { return p.findings[i].Key() < p.findings[j].Key() })
	return &Result{Findings: p.findings, Renders: p.renders.Load(), Base: base, Noise: noise}, nil
}

// baseline renders the same values twice; fields that differ between the two
// renders (random secrets, generated certs, their checksums) become noise.
func (p *prober) baseline(render RenderFunc, vals map[string]interface{}) (compare.Snapshot, map[string]bool, error) {
	a, err := p.render(render, values.CopyMap(vals))
	if err != nil {
		return nil, nil, err
	}
	b, err := p.render(render, values.CopyMap(vals))
	if err != nil {
		return nil, nil, err
	}
	return a, compare.Noise(a, b), nil
}

func (p *prober) render(render RenderFunc, vals map[string]interface{}) (compare.Snapshot, error) {
	p.sem <- struct{}{}
	defer func() { <-p.sem }()
	p.renders.Add(1)
	return render(vals)
}

func (p *prober) differs(render RenderFunc, vals map[string]interface{}, base compare.Snapshot, noise map[string]bool) (bool, error) {
	snap, err := p.render(render, vals)
	if err != nil {
		return true, err
	}
	return !compare.Equal(base, snap, noise), nil
}

func (p *prober) children(path []string, m map[string]interface{}) bool {
	var wg sync.WaitGroup
	var any atomic.Bool
	for k, v := range m {
		child := append(append([]string{}, path...), k)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.probe(child, v) {
				any.Store(true)
			}
		}()
	}
	wg.Wait()
	return any.Load()
}

// probe bisects the value tree: if removing a whole subtree changes nothing,
// every leaf under it is classified at once; otherwise it descends.
func (p *prober) probe(path []string, sub interface{}) bool {
	mutated := values.CopyMap(p.User)
	values.Delete(mutated, path)
	changed, err := p.differs(p.Render, mutated, p.base, p.noise)
	m, isMap := sub.(map[string]interface{})
	isMap = isMap && len(m) > 0
	key := values.Display(path)

	if err != nil {
		p.Logf("remove %s: render fails: %s", key, firstLine(err))
		if isMap && p.children(path, m) {
			return true
		}
		p.replaceUnder(path, Finding{Path: path, Status: Error, Value: sub, Err: firstLine(err)})
		return true
	}
	if !changed {
		p.Logf("remove %s: no change", key)
		p.classify(path, sub)
		return false
	}
	p.Logf("remove %s: output changes", key)
	if isMap {
		if p.children(path, m) {
			return true
		}
		// The subtree matters but no single child does on its own,
		// e.g. `{{ if or .a .b }}`. Report the group, not dead children.
		p.replaceUnder(path, Finding{Path: path, Status: Group, Value: sub})
		return true
	}
	p.add(Finding{Path: path, Status: Alive, Value: sub})
	return true
}

func (p *prober) classify(path []string, sub interface{}) {
	if m, ok := sub.(map[string]interface{}); ok && len(m) > 0 {
		var wg sync.WaitGroup
		for k, v := range m {
			child := append(append([]string{}, path...), k)
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.classify(child, v)
			}()
		}
		wg.Wait()
		return
	}

	def, hasDef := values.Lookup(p.Defaults, path)
	f := Finding{Path: path, Status: Dead, Value: sub, Default: def, HasDefault: hasDef}
	switch {
	case (hasDef && sameValue(def, sub)) || (!hasDef && sub == nil):
		f.Status = Redundant
	case p.sentinelChanges(path, sub):
		// Removing the key changed nothing, but another value does: the chart
		// reads it and the user value matches a fallback in the template,
		// e.g. `default "ADMIN_TOKEN" .Values.x`.
		f.Status = Redundant
		f.Implicit = true
	default:
		if t := p.conditional(path); t != nil {
			f.Status = Conditional
			f.Toggle = t
		} else if apis := p.apiConditional(path); apis != nil {
			f.Status = Conditional
			f.RequiresAPI = apis
		}
	}
	p.Logf("classify %s: %s", values.Display(path), f.Status)
	p.add(f)
}

func (p *prober) sentinelChanges(path []string, v interface{}) bool {
	mutated := values.CopyMap(p.User)
	values.SetPath(mutated, path, sentinel(v))
	changed, _ := p.differs(p.Render, mutated, p.base, p.noise)
	return changed
}

// removalMatters reports whether removing path changes the output of the
// variant's baseline.
func (p *prober) removalMatters(v *variant, path []string) bool {
	mutated := values.CopyMap(v.user)
	values.Delete(mutated, path)
	changed, err := p.differs(v.render, mutated, v.base, v.noise)
	return err == nil && changed
}

// conditional looks for a disabled `enabled` flag that gates the key: first
// next to the key and on its ancestors, then every `*.enabled: false` the
// user set explicitly (e.g. kubeEtcd.enabled gating defaultRules.rules.etcd).
// It turns the flag on and checks whether the key starts to matter.
func (p *prober) conditional(path []string) []string {
	tried := map[string]bool{values.Key(path): true}
	var candidates [][]string
	for i := len(path) - 1; i >= 1; i-- {
		candidates = append(candidates, append(append([]string{}, path[:i]...), "enabled"))
	}
	candidates = append(candidates, p.userToggles...)

	for _, toggle := range candidates {
		if tried[values.Key(toggle)] {
			continue
		}
		tried[values.Key(toggle)] = true
		v, ok := values.Lookup(p.User, toggle)
		if !ok {
			v, ok = values.Lookup(p.Defaults, toggle)
		}
		if b, isBool := v.(bool); !ok || !isBool || b {
			continue
		}
		ctx := p.toggled(toggle)
		if ctx.ok && p.removalMatters(ctx, path) {
			return toggle
		}
	}
	return nil
}

// apiConditional checks whether the key starts to matter when the cluster
// serves the API versions the chart looks for. It first tries all of them at
// once, then each one alone to name the API that gates the key.
func (p *prober) apiConditional(path []string) []string {
	if p.RenderWithAPIs == nil || len(p.MissingAPIs) == 0 {
		return nil
	}
	all := p.withAPIs(p.MissingAPIs)
	if !all.ok || !p.removalMatters(all, path) {
		return nil
	}
	for _, api := range p.MissingAPIs {
		ctx := p.withAPIs([]string{api})
		if ctx.ok && p.removalMatters(ctx, path) {
			return []string{api}
		}
	}
	return append([]string{}, p.MissingAPIs...)
}

func (p *prober) toggled(toggle []string) *variant {
	return p.variant("toggle\x01"+values.Key(toggle), func() (map[string]interface{}, RenderFunc) {
		u := values.CopyMap(p.User)
		values.SetPath(u, toggle, true)
		return u, p.Render
	})
}

func (p *prober) withAPIs(apis []string) *variant {
	return p.variant("api\x01"+strings.Join(apis, ","), func() (map[string]interface{}, RenderFunc) {
		return p.User, func(v map[string]interface{}) (compare.Snapshot, error) {
			return p.RenderWithAPIs(v, apis)
		}
	})
}

func (p *prober) variant(key string, build func() (map[string]interface{}, RenderFunc)) *variant {
	v, _ := p.variants.LoadOrStore(key, &variant{})
	ctx := v.(*variant)
	ctx.once.Do(func() {
		user, render := build()
		base, noise, err := p.baseline(render, user)
		if err != nil {
			p.Logf("variant %s: render fails: %s", strings.ReplaceAll(key, "\x01", " "), firstLine(err))
			return
		}
		ctx.user, ctx.render, ctx.base, ctx.noise, ctx.ok = user, render, base, noise, true
	})
	return ctx
}

func (p *prober) add(f Finding) {
	p.mu.Lock()
	p.findings = append(p.findings, f)
	p.mu.Unlock()
}

// replaceUnder drops the findings already recorded for keys under path and
// records f for path itself. Children are done by then (children() waits).
func (p *prober) replaceUnder(path []string, f Finding) {
	prefix := values.Key(path) + "\x00"
	p.mu.Lock()
	kept := p.findings[:0]
	for _, x := range p.findings {
		if !strings.HasPrefix(values.Key(x.Path)+"\x00", prefix) {
			kept = append(kept, x)
		}
	}
	p.findings = append(kept, f)
	p.mu.Unlock()
}

func sentinel(v interface{}) interface{} {
	switch t := v.(type) {
	case bool:
		return !t
	case float64:
		return t + 7919
	case int64:
		return t + 7919
	case int:
		return t + 7919
	case []interface{}:
		if len(t) > 0 {
			return []interface{}{}
		}
		return []interface{}{"deadvalues-sentinel"}
	case map[string]interface{}:
		return map[string]interface{}{"deadvalues-sentinel": "deadvalues-sentinel"}
	default:
		return "deadvalues-sentinel"
	}
}

// sameValue compares through JSON so int64 from --set and float64 from YAML
// files are equal when they hold the same number.
func sameValue(a, b interface{}) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}
