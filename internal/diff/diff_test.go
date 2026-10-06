package diff_test

import (
	"context"
	"testing"

	"github.com/guarnz/deadvalues/internal/compare"
	"github.com/guarnz/deadvalues/internal/diff"
	"github.com/guarnz/deadvalues/internal/probe"
	"github.com/guarnz/deadvalues/internal/render"
	"github.com/guarnz/deadvalues/internal/source"
	"github.com/guarnz/deadvalues/internal/values"
)

type side struct {
	render   probe.RenderFunc
	defaults map[string]interface{}
	result   *probe.Result
}

func analyze(t *testing.T, chartPath string, user map[string]interface{}) side {
	t.Helper()
	c, err := source.NewLoader("", true).Load(context.Background(), source.Ref{Chart: chartPath})
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.New(c, render.Options{Release: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := r.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	fn := func(v map[string]interface{}) (compare.Snapshot, error) {
		m, err := r.Render(v)
		if err != nil {
			return nil, err
		}
		return compare.Flatten(m)
	}
	res, err := probe.Run(probe.Options{Render: fn, User: user, Defaults: defaults, Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	return side{render: fn, defaults: defaults, result: res}
}

func TestTransitionsBetweenVersions(t *testing.T) {
	set := values.New()
	if err := set.AddFile("../../testdata/values/cases.yaml"); err != nil {
		t.Fatal(err)
	}
	old := analyze(t, "../../testdata/charts/cases", set.Values)
	cur := analyze(t, "../../testdata/charts/cases-v2", set.Values)

	ts := diff.Keys(old.result, cur.result, old.defaults, cur.defaults)
	changed, _ := diff.ChangedDefaults(diff.ChangedOptions{
		OldDefaults: old.defaults,
		NewDefaults: cur.defaults,
		User:        set.Values,
		Render:      cur.render,
		Base:        cur.result.Base,
		Noise:       cur.result.Noise,
		Workers:     4,
	})
	ts = append(ts, changed...)

	got := map[string]diff.Kind{}
	for _, tr := range ts {
		got[tr.Key()] = tr.Kind
	}
	want := map[string]diff.Kind{
		"labels.team":               diff.Breaking,
		"service.type":              diff.Pinned,
		"revisionHistoryLimit":      diff.Changed,
		"monitoring.serviceMonitor": diff.Fixed,
	}
	for k, kind := range want {
		if g, ok := got[k]; !ok || g != kind {
			t.Errorf("%s: got %v (present=%v), want %v", k, g, ok, kind)
		}
	}
	if len(got) != len(want) {
		t.Errorf("transitions = %v, want exactly %v", got, want)
	}
}
