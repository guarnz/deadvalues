// Package argo reads Argo CD Application manifests and turns their Helm
// sources into chart + values targets.
package argo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/guarnz/deadvalues/internal/source"
)

type Application struct {
	File      string
	Name      string
	Namespace string
	Sources   []Source
}

type Source struct {
	RepoURL        string `json:"repoURL"`
	Chart          string `json:"chart"`
	TargetRevision string `json:"targetRevision"`
	Path           string `json:"path"`
	Ref            string `json:"ref"`
	Helm           *Helm  `json:"helm"`
}

type Helm struct {
	ReleaseName             string                 `json:"releaseName"`
	ValueFiles              []string               `json:"valueFiles"`
	Values                  string                 `json:"values"`
	ValuesObject            map[string]interface{} `json:"valuesObject"`
	Parameters              []Parameter            `json:"parameters"`
	IgnoreMissingValueFiles bool                   `json:"ignoreMissingValueFiles"`
}

type Parameter struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	ForceString bool   `json:"forceString"`
}

type manifest struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Source      *Source  `json:"source"`
		Sources     []Source `json:"sources"`
		Destination struct {
			Namespace string `json:"namespace"`
		} `json:"destination"`
	} `json:"spec"`
}

// Target is one Helm source of an Application, resolved against a local
// checkout: a chart reference plus the values Argo CD would pass to it.
type Target struct {
	App         string
	Name        string
	SourceIndex int
	Chart       source.Ref
	Release     string
	Namespace   string
	ValueFiles  []string
	Values      string
	ValuesObj   map[string]interface{}
	Parameters  []Parameter
}

var docSeparator = regexp.MustCompile(`(?m)^---\s*$`)

func Load(path string) (*Application, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	app, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	app.File = path
	return app, nil
}

// ErrNotApplication is returned for YAML files that hold no Application.
var ErrNotApplication = fmt.Errorf("no Argo CD Application found")

func Parse(data []byte) (*Application, error) {
	for _, doc := range docSeparator.Split(string(data), -1) {
		var m manifest
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			continue
		}
		if m.Kind != "Application" {
			continue
		}
		app := &Application{Name: m.Metadata.Name, Namespace: m.Spec.Destination.Namespace}
		if m.Spec.Source != nil {
			app.Sources = append(app.Sources, *m.Spec.Source)
		}
		app.Sources = append(app.Sources, m.Spec.Sources...)
		return app, nil
	}
	return nil, ErrNotApplication
}

// Targets resolves every Helm source of the Application. `$ref/...` value
// files and local chart paths are resolved against repoRoot, the root of the
// checkout that holds the Application.
func (a *Application) Targets(repoRoot string) ([]Target, error) {
	refs := map[string]bool{}
	for _, s := range a.Sources {
		if s.Ref != "" {
			refs[s.Ref] = true
		}
	}

	var out []Target
	for i, s := range a.Sources {
		if s.Ref != "" && s.Chart == "" && s.Path == "" {
			continue
		}
		t := Target{App: a.Name, SourceIndex: i, Namespace: a.Namespace, Release: a.Name}
		local := false
		switch {
		case s.Chart != "":
			t.Chart = source.Ref{Repo: s.RepoURL, Chart: s.Chart, Version: s.TargetRevision}
			t.Name = s.Chart
		case s.Path != "":
			dir := filepath.Join(repoRoot, filepath.FromSlash(s.Path))
			if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
				continue
			}
			t.Chart = source.Ref{Chart: dir}
			t.Name = filepath.Base(dir)
			local = true
		default:
			continue
		}

		if h := s.Helm; h != nil {
			if h.ReleaseName != "" {
				t.Release = h.ReleaseName
			}
			for _, vf := range h.ValueFiles {
				p, err := resolveValueFile(vf, repoRoot, s, local, refs)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", a.Name, err)
				}
				if _, err := os.Stat(p); err != nil {
					if h.IgnoreMissingValueFiles {
						continue
					}
					return nil, fmt.Errorf("%s: value file %s: %w", a.Name, vf, err)
				}
				t.ValueFiles = append(t.ValueFiles, p)
			}
			t.Values = h.Values
			t.ValuesObj = h.ValuesObject
			t.Parameters = h.Parameters
		}
		out = append(out, t)
	}

	if len(out) > 1 {
		for i := range out {
			out[i].Name = a.Name + "/" + out[i].Name
		}
	} else if len(out) == 1 {
		out[0].Name = a.Name
	}
	return out, nil
}

func resolveValueFile(vf, repoRoot string, s Source, local bool, refs map[string]bool) (string, error) {
	if strings.HasPrefix(vf, "$") {
		ref, rest, _ := strings.Cut(strings.TrimPrefix(vf, "$"), "/")
		if !refs[ref] {
			return "", fmt.Errorf("value file %s: no source with ref %q", vf, ref)
		}
		return filepath.Join(repoRoot, filepath.FromSlash(rest)), nil
	}
	if !local {
		return "", fmt.Errorf("value file %s is relative to a remote chart; only $ref/ paths are supported", vf)
	}
	return filepath.Join(repoRoot, filepath.FromSlash(s.Path), filepath.FromSlash(vf)), nil
}

// RepoRoot returns the top level of the git checkout holding path, or the
// directory of path when it is not inside a git repository.
func RepoRoot(path string) string {
	dir := filepath.Dir(path)
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		abs, _ := filepath.Abs(dir)
		return abs
	}
	return filepath.FromSlash(strings.TrimSpace(string(out)))
}

// ChangedFiles lists files changed between rev and the working tree,
// as absolute paths.
func ChangedFiles(repoRoot, rev string) (map[string]bool, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--name-only", rev)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %s", rev, strings.TrimSpace(stderr.String()))
	}
	files := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files[filepath.Join(repoRoot, filepath.FromSlash(line))] = true
		}
	}
	return files, nil
}
