// Package diff compares how the same values behave against two versions of
// a chart.
package diff

import (
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/guarnz/deadvalues/internal/compare"
	"github.com/guarnz/deadvalues/internal/probe"
	"github.com/guarnz/deadvalues/internal/values"
)

type Kind int

const (
	Breaking Kind = iota
	Pinned
	Changed
	Fixed
)

func (k Kind) String() string {
	return [...]string{"BREAKING", "PINNED", "CHANGED", "FIXED"}[k]
}

type Transition struct {
	Path       []string
	Kind       Kind
	From, To   probe.Status
	Value      interface{}
	OldDefault interface{}
	NewDefault interface{}
	HasValue   bool
}

func (t Transition) Key() string { return values.Display(t.Path) }

// Keys reports how every key the user sets moved between the two versions.
func Keys(old, new *probe.Result, oldDefaults, newDefaults map[string]interface{}) []Transition {
	paths := map[string][]string{}
	for _, f := range old.Findings {
		paths[values.Key(f.Path)] = f.Path
	}
	for _, f := range new.Findings {
		paths[values.Key(f.Path)] = f.Path
	}

	var out []Transition
	for _, path := range paths {
		from, ok1 := old.Find(path)
		to, ok2 := new.Find(path)
		if !ok1 || !ok2 {
			continue
		}
		t := Transition{Path: path, From: from.Status, To: to.Status, Value: to.Value, HasValue: true}
		t.OldDefault, _ = values.Lookup(oldDefaults, path)
		t.NewDefault, _ = values.Lookup(newDefaults, path)
		switch {
		case from.Status.Effective() && (to.Status == probe.Dead || to.Status == probe.Conditional):
			t.Kind = Breaking
		case from.Status == probe.Redundant && to.Status.Effective():
			t.Kind = Pinned
		case (from.Status == probe.Dead || from.Status == probe.Conditional) && to.Status.Effective():
			t.Kind = Fixed
		default:
			continue
		}
		out = append(out, t)
	}
	sortTransitions(out)
	return out
}

type ChangedOptions struct {
	OldDefaults map[string]interface{}
	NewDefaults map[string]interface{}
	User        map[string]interface{}
	Render      probe.RenderFunc
	Base        compare.Snapshot
	Noise       map[string]bool
	Workers     int
}

// ChangedDefaults finds defaults the user does not set whose change between
// versions alters the rendered output of the new version: it renders the new
// chart with the old default forced back and compares.
func ChangedDefaults(o ChangedOptions) ([]Transition, int64) {
	type candidate struct {
		path     []string
		old, new interface{}
	}
	var candidates []candidate
	values.Leaves(o.OldDefaults, nil, func(path []string, ov interface{}) {
		nv, ok := values.Lookup(o.NewDefaults, path)
		if !ok || same(ov, nv) || userSets(o.User, path) {
			return
		}
		candidates = append(candidates, candidate{path: path, old: ov, new: nv})
	})

	if o.Workers < 1 {
		o.Workers = 1
	}
	var (
		mu      sync.Mutex
		out     []Transition
		renders atomic.Int64
		wg      sync.WaitGroup
		sem     = make(chan struct{}, o.Workers)
	)
	for _, c := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			mutated := values.CopyMap(o.User)
			values.SetPath(mutated, c.path, values.Copy(c.old))
			renders.Add(1)
			snap, err := o.Render(mutated)
			if err != nil || compare.Equal(o.Base, snap, o.Noise) {
				return
			}
			mu.Lock()
			out = append(out, Transition{Path: c.path, Kind: Changed, OldDefault: c.old, NewDefault: c.new})
			mu.Unlock()
		}()
	}
	wg.Wait()
	sortTransitions(out)
	return out, renders.Load()
}

func userSets(user map[string]interface{}, path []string) bool {
	for i := 1; i <= len(path); i++ {
		v, ok := values.Lookup(user, path[:i])
		if !ok {
			return false
		}
		if _, isMap := v.(map[string]interface{}); !isMap || i == len(path) {
			return true
		}
	}
	return false
}

func same(a, b interface{}) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// Sort orders transitions by kind (BREAKING first), then by key.
func Sort(ts []Transition) { sortTransitions(ts) }

func sortTransitions(ts []Transition) {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].Kind != ts[j].Kind {
			return ts[i].Kind < ts[j].Kind
		}
		return ts[i].Key() < ts[j].Key()
	})
}
