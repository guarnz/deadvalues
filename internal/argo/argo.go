// Package argo reads Argo CD Application manifests and turns their Helm
// sources into chart + values targets.
package argo

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/guarnz/deadvalues/internal/git"
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
	Namespace               string                 `json:"namespace"`
	KubeVersion             string                 `json:"kubeVersion"`
	APIVersions             []string               `json:"apiVersions"`
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
	// KubeVersion and APIVersions replace the cluster's when the source sets them, as in Argo CD.
	KubeVersion string
	APIVersions []string
	// Skip is why a Helm source cannot be analyzed yet; it is reported, not rendered.
	Skip string
}

var docSeparator = regexp.MustCompile(`(?m)^---\s*$`)

// Load reads every Argo CD Application in the file at path.
func Load(path string) ([]*Application, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	apps, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, app := range apps {
		app.File = path
	}
	return apps, nil
}

// ErrNotApplication is returned for YAML files that hold no Application.
var ErrNotApplication = fmt.Errorf("no Argo CD Application found")

// Parse returns every Application in the YAML documents, in file order.
func Parse(data []byte) ([]*Application, error) {
	var apps []*Application
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
		apps = append(apps, app)
	}
	if len(apps) == 0 {
		return nil, ErrNotApplication
	}
	return apps, nil
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
				if s.Helm != nil {
					t.Name = path.Base(strings.TrimSuffix(s.RepoURL, ".git"))
					t.Skip = fmt.Sprintf("Helm chart at %s in %s is not in this repository; charts from other git repositories are not supported yet", s.Path, s.RepoURL)
					out = append(out, t)
				}
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
			if h.Namespace != "" {
				t.Namespace = h.Namespace
			}
			t.KubeVersion, t.APIVersions = h.KubeVersion, h.APIVersions
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
	out, err := git.Command(dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		abs, _ := filepath.Abs(dir)
		return abs
	}
	return filepath.FromSlash(strings.TrimSpace(string(out)))
}

// ChangedFiles lists files changed in the working tree since it left rev
// (their merge base, or rev itself in a shallow clone), as absolute paths.
func ChangedFiles(repoRoot, rev string) (map[string]bool, error) {
	from := rev
	if out, err := git.Command(repoRoot, "merge-base", rev, "HEAD").Output(); err == nil {
		from = strings.TrimSpace(string(out))
	}
	var stderr bytes.Buffer
	cmd := git.Command(repoRoot, "diff", "--name-only", from)
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
