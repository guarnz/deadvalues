package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/guarnz/deadvalues/internal/flux"
	"github.com/guarnz/deadvalues/internal/render"
)

// chartDownloads is how many charts download at once; it waits on the network, not the CPU.
const chartDownloads = 8

// crdSearch finds the APIs a repository installs itself, per cluster, when a chart asks
// for one the cluster is not known to serve. Nothing is read until that happens.
type crdSearch struct {
	root     string
	jobs     []job
	loaded   bool
	clusters map[string]*clusterCRDs
	used     map[string]string
}

// clusterCRDs is what was found for one cluster: API -> what installs it.
type clusterCRDs struct {
	served    map[string]string
	loose     bool
	preloaded bool
	renderers map[string]*render.Renderer
	rendered  map[string]bool
}

func newCRDSearch(root string) *crdSearch {
	return &crdSearch{root: root, clusters: map[string]*clusterCRDs{}, used: map[string]string{}}
}

func sameCluster(a, b string) bool { return a == b || a == "" || b == "" }

func apiGroup(api string) string {
	group, _, _ := strings.Cut(api, "/")
	return group
}

// repoAPIs returns the missing APIs that the repository installs in j's cluster. Only
// groups with a dot can be a CRD; the others are built into Kubernetes.
func (g *globals) repoAPIs(ctx context.Context, j job, missing []string) []string {
	s := g.crds
	if s == nil {
		return nil
	}
	missing = slices.DeleteFunc(slices.Clone(missing), func(api string) bool {
		return !strings.Contains(apiGroup(api), ".")
	})
	if len(missing) == 0 {
		return nil
	}
	if !s.loaded {
		s.loaded = true
		s.jobs, _, _ = g.dirJobs(s.root, s.root, nil)
	}
	c := s.clusters[j.cluster]
	if c == nil {
		c = &clusterCRDs{served: map[string]string{}, renderers: map[string]*render.Renderer{}, rendered: map[string]bool{}}
		s.clusters[j.cluster] = c
	}
	g.readLooseCRDs(s, j.cluster, c)
	if len(c.pending(missing)) > 0 && !c.preloaded {
		c.preloaded = true
		g.preloadCharts(ctx, s.jobs, j.cluster)
	}
	for _, other := range s.jobs {
		pending := c.pending(missing)
		if len(pending) == 0 {
			break
		}
		if other.skip == "" && sameCluster(other.cluster, j.cluster) {
			g.readChartCRDs(ctx, other, c, pending)
		}
	}
	var found []string
	for _, api := range missing {
		if origin, ok := c.served[api]; ok {
			found = append(found, api)
			s.used[strings.TrimPrefix(strings.TrimPrefix(j.cluster, "flux|"), "argo|")+"\x00"+api] = origin
		}
	}
	return found
}

func (c *clusterCRDs) pending(apis []string) []string {
	var out []string
	for _, api := range apis {
		if _, ok := c.served[api]; !ok {
			out = append(out, api)
		}
	}
	return out
}

func (c *clusterCRDs) add(objs []*flux.Object, origin string) {
	for _, o := range objs {
		if o.Kind != "CustomResourceDefinition" {
			continue
		}
		for _, api := range flux.CRDAPIs(o) {
			if _, ok := c.served[api]; !ok {
				c.served[api] = origin
			}
		}
	}
}

// readLooseCRDs reads the CRDs applied as plain manifests: what the Flux graph builds for
// the cluster, the source paths of its Applications, or else the whole repository.
func (g *globals) readLooseCRDs(s *crdSearch, cluster string, c *clusterCRDs) {
	if c.loose {
		return
	}
	c.loose = true
	graph := g.fluxGraph(s.root)
	for _, a := range graph.Applied {
		if a.Err == nil && sameCluster("flux|"+a.Cluster, cluster) {
			c.add(a.Objects, relPath(s.root, a.Dir))
		}
	}
	var paths []string
	for _, j := range s.jobs {
		if sameCluster(j.cluster, cluster) {
			paths = append(paths, j.paths...)
		}
	}
	for _, o := range g.repoIndex(s.root).Objects {
		if o.Kind != "CustomResourceDefinition" {
			continue
		}
		inPath := len(graph.Applied) == 0 && len(paths) == 0
		for _, p := range paths {
			inPath = inPath || underDir(p, o.File)
		}
		if inPath {
			c.add([]*flux.Object{o}, relPath(s.root, o.File))
		}
	}
}

// preloadCharts downloads the charts of a cluster at once into the cache: the search reads
// them one at a time, and a CI run usually starts with an empty cache.
func (g *globals) preloadCharts(ctx context.Context, jobs []job, cluster string) {
	if g.loader.NoCache {
		return
	}
	seen := map[string]bool{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, chartDownloads)
	for _, j := range jobs {
		if j.skip != "" || j.ref.IsLocal() || seen[j.ref.String()] || !sameCluster(j.cluster, cluster) {
			continue
		}
		seen[j.ref.String()] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, _ = g.loader.Load(ctx, j.ref)
		}()
	}
	wg.Wait()
}

// readChartCRDs reads a chart's crds/ once, and renders it with the app's values only when
// a template declares a CRD of a group still missing, so that flags like crds.enabled count.
func (g *globals) readChartCRDs(ctx context.Context, j job, c *clusterCRDs, pending []string) {
	r, seen := c.renderers[j.ident]
	if !seen {
		var err error
		if r, err = g.renderer(ctx, j, j.ref); err != nil {
			r = nil
		}
		c.renderers[j.ident] = r
		if r != nil {
			for _, data := range r.CRDFiles() {
				if objs, err := flux.ParseYAML(data); err == nil {
					c.add(objs, j.name)
				}
			}
		}
	}
	if r == nil || c.rendered[j.ident] || len(c.pending(pending)) == 0 {
		return
	}
	for _, api := range c.pending(pending) {
		if !r.DeclaresCRD(apiGroup(api)) {
			continue
		}
		c.rendered[j.ident] = true
		manifests, err := r.Render(j.vals.Values)
		if err != nil {
			return
		}
		for _, content := range manifests {
			if objs, err := flux.ParseYAML([]byte(content)); err == nil {
				c.add(objs, j.name)
			}
		}
		return
	}
}

// report lists the APIs taken from the repository, so that a wrong guess can be seen.
func (s *crdSearch) report(w io.Writer) {
	if s == nil || len(s.used) == 0 {
		return
	}
	lines := make([]string, 0, len(s.used))
	for key, origin := range s.used {
		cluster, api, _ := strings.Cut(key, "\x00")
		if parts := strings.Split(api, "/"); len(parts) > 2 {
			api = parts[0] + "/" + parts[1]
		}
		line := api + " (" + origin + ")"
		if cluster != "" {
			line = cluster + ": " + line
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	fmt.Fprintln(w, "APIs served by CRDs in the repository: "+strings.Join(slices.Compact(lines), ", "))
}
