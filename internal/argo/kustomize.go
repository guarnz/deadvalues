package argo

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/types"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/guarnz/deadvalues/internal/source"
)

var kustomizationNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// kustomization holds the parts of a kustomization file that decide its Helm charts.
type kustomization struct {
	Resources             []string           `json:"resources"`
	Components            []string           `json:"components"`
	Patches               []interface{}      `json:"patches"`
	PatchesStrategicMerge []interface{}      `json:"patchesStrategicMerge"`
	PatchesJSON6902       []interface{}      `json:"patchesJson6902"`
	HelmCharts            []types.HelmChart  `json:"helmCharts"`
	HelmGlobals           *types.HelmGlobals `json:"helmGlobals"`
}

// Layer is one values source of a helmCharts entry: a values file, or the
// valuesInline block with its node so keys keep their line in the kustomization.
type Layer struct {
	File   string
	Node   *yaml.Node
	Prefix []string
}

// KustomizationFile returns the kustomization file in dir, or "" when there is none.
func KustomizationFile(dir string) string {
	for _, n := range kustomizationNames {
		p := filepath.Join(dir, n)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func isRemote(r string) bool {
	return strings.Contains(r, "://") || strings.HasPrefix(r, "github.com/") || strings.Contains(r, "?ref=")
}

// kustomizeTargets turns the helmCharts of the kustomization in dir, its local bases
// and components into targets, rendered with the Helm SDK instead of the helm binary.
func (a *Application) kustomizeTargets(dir string, index int, s Source) ([]Target, error) {
	var out []Target
	var files []string
	patched := s.Kustomize != nil && len(s.Kustomize.Patches) > 0
	seen := map[string]bool{}
	var walk func(dir string) error
	walk = func(dir string) error {
		dir = filepath.Clean(dir)
		if seen[dir] {
			return nil
		}
		seen[dir] = true
		file := KustomizationFile(dir)
		if file == "" {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		files = append(files, file)
		var k kustomization
		if err := sigsyaml.Unmarshal(data, &k); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		patched = patched || len(k.Patches)+len(k.PatchesStrategicMerge)+len(k.PatchesJSON6902) > 0
		for _, r := range append(append([]string{}, k.Resources...), k.Components...) {
			if isRemote(r) {
				continue
			}
			if err := walk(filepath.Join(dir, r)); err != nil {
				return err
			}
		}
		chartHome := types.HelmDefaultHome
		if k.HelmGlobals != nil && k.HelmGlobals.ChartHome != "" {
			chartHome = k.HelmGlobals.ChartHome
		}
		for j, hc := range k.HelmCharts {
			t, err := a.helmChartTarget(dir, file, chartHome, hc, inlineValues(&doc, j), j)
			if err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			t.SourceIndex = index
			out = append(out, t)
		}
		return nil
	}
	if err := walk(dir); err != nil {
		return nil, err
	}
	if s.Kustomize != nil {
		for _, c := range s.Kustomize.Components {
			if isRemote(c) {
				continue
			}
			if err := walk(filepath.Join(dir, c)); err != nil {
				return nil, err
			}
		}
	}
	names := map[string]int{}
	for _, t := range out {
		names[t.Name]++
	}
	taken := map[string]int{}
	for i := range out {
		if names[out[i].Name] > 1 {
			out[i].Name += "/" + path.Base(filepath.ToSlash(out[i].Chart.Chart))
		}
		if taken[out[i].Name]++; taken[out[i].Name] > 1 {
			out[i].Name += "#" + strconv.Itoa(taken[out[i].Name])
		}
		out[i].Files = files
		if patched {
			out[i].Notes = append(out[i].Notes, "kustomize patches run after the chart is rendered and are not considered")
		}
	}
	return out, nil
}

// helmChartTarget builds the target of one helmCharts entry, with values layered
// in the order kustomize hands them to helm template.
func (a *Application) helmChartTarget(dir, file, chartHome string, hc types.HelmChart, inline *yaml.Node, j int) (Target, error) {
	t := Target{App: a.Name, Name: hc.Name, Release: hc.Name, Namespace: hc.Namespace, KubeVersion: hc.KubeVersion, APIVersions: hc.ApiVersions}
	if hc.ReleaseName != "" {
		t.Name, t.Release = hc.ReleaseName, hc.ReleaseName
	}
	if hc.Repo != "" {
		t.Chart = source.Ref{Repo: hc.Repo, Chart: hc.Name, Version: hc.Version}
	} else {
		t.Chart = source.Ref{Chart: filepath.Join(inDir(dir, chartHome), hc.Name)}
	}

	fileLayer := Layer{File: inDir(dir, hc.ValuesFile)}
	inlineLayer := Layer{File: file, Node: inline, Prefix: []string{"helmCharts", strconv.Itoa(j), "valuesInline"}}
	hasFile, hasInline := hc.ValuesFile != "", inline != nil
	switch hc.ValuesMerge {
	case "", "override":
		if hasFile {
			t.Layers = append(t.Layers, fileLayer)
		}
		if hasInline {
			t.Layers = append(t.Layers, inlineLayer)
		}
	case "merge":
		if hasInline {
			t.Layers = append(t.Layers, inlineLayer)
		}
		if hasFile {
			t.Layers = append(t.Layers, fileLayer)
		}
	case "replace":
		if hasInline {
			t.Layers = append(t.Layers, inlineLayer)
		} else if hasFile {
			t.Layers = append(t.Layers, fileLayer)
		}
	default:
		return t, fmt.Errorf("helmCharts %s: valuesMerge must be merge, override or replace, got %q", hc.Name, hc.ValuesMerge)
	}
	for _, f := range hc.AdditionalValuesFiles {
		t.Layers = append(t.Layers, Layer{File: inDir(dir, f)})
	}
	return t, nil
}

func inDir(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, filepath.FromSlash(p))
}

// inlineValues returns the valuesInline mapping of the j-th helmCharts entry, or nil.
func inlineValues(doc *yaml.Node, j int) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	charts := mapValue(doc.Content[0], "helmCharts")
	if charts == nil || charts.Kind != yaml.SequenceNode || j >= len(charts.Content) {
		return nil
	}
	v := mapValue(charts.Content[j], "valuesInline")
	if v == nil || v.Kind != yaml.MappingNode || len(v.Content) == 0 {
		return nil
	}
	return v
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
