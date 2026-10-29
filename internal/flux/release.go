package flux

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
	"helm.sh/helm/v4/pkg/chart/common"

	"github.com/guarnz/deadvalues/internal/git"
	"github.com/guarnz/deadvalues/internal/source"
	"github.com/guarnz/deadvalues/internal/values"
)

// IsRelease reports whether o is a full HelmRelease. Partial HelmReleases
// used as kustomize patches have neither chart nor chartRef.
func IsRelease(o *Object) bool {
	return o.Kind == "HelmRelease" && (Get(o.Node, "spec", "chart") != nil || Get(o.Node, "spec", "chartRef") != nil)
}

type Release struct {
	Object      *Object
	ID          string
	Chart       source.Ref
	ReleaseName string
	Namespace   string
	Values      *values.Set
	Notes       []string
	// Placeholders are the targetPaths of valuesFrom sources not in the repository.
	Placeholders [][]string
	// Skip is why the release cannot be analyzed yet; it is reported, not rendered.
	Skip string
}

// otherRepository is a chart that lives in a git repository other than this checkout.
type otherRepository struct{ reason string }

func (e *otherRepository) Error() string { return e.reason }

// Resolver turns HelmReleases into releases, with sources, ConfigMaps and Secrets from
// Sources; in build mode, Attribute traces kustomize output values back to files.
type Resolver struct {
	RepoRoot string
	Sources  *Index
	// Fallback is searched when Sources has no match, e.g. the whole
	// repository when Sources is a kustomize build output.
	Fallback  *Index
	Attribute func(rel *Object, path []string) []values.Origin
	// Namespace replaces the releases' own, like a Kustomization's targetNamespace.
	Namespace string
	// Origin is the origin remote of the checkout; a GitRepository with another URL is not here.
	Origin string
}

func (r *Resolver) find(kind, name, namespace, near string) *Object {
	if o := r.Sources.Find(kind, name, namespace, near); o != nil {
		return o
	}
	if r.Fallback != nil {
		return r.Fallback.Find(kind, name, namespace, near)
	}
	return nil
}

func (r *Resolver) Resolve(rel *Object) (*Release, error) {
	spec := Get(rel.Node, "spec")
	ns := rel.Namespace
	if r.Namespace != "" {
		ns = r.Namespace
	}
	if ns == "" {
		ns = "default"
	}
	out := &Release{Object: rel, ID: ns + "/" + rel.Name, Values: values.New()}

	ref, err := r.chart(rel, spec, ns)
	var other *otherRepository
	if errors.As(err, &other) {
		out.Skip = other.reason
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", out.ID, err)
	}
	out.Chart = ref
	if files := Get(spec, "chart", "spec", "valuesFiles"); files != nil && len(files.Content) > 0 {
		out.Notes = append(out.Notes, "chart.spec.valuesFiles is not applied yet; keys those files set are not taken into account")
	}

	target := Str(spec, "targetNamespace")
	out.ReleaseName = Str(spec, "releaseName")
	if out.ReleaseName == "" {
		out.ReleaseName = rel.Name
		if target != "" {
			out.ReleaseName = target + "-" + rel.Name
		}
	}
	out.Namespace = ns
	if target != "" {
		out.Namespace = target
	}

	if from := Get(spec, "valuesFrom"); from != nil && from.Kind == yaml.SequenceNode {
		for _, vf := range from.Content {
			if err := r.valuesFrom(out, rel, vf, ns); err != nil {
				return nil, fmt.Errorf("%s: %w", out.ID, err)
			}
		}
	}

	if v := Get(spec, "values"); v != nil && v.Kind == yaml.MappingNode {
		if r.Attribute == nil && rel.File != "" {
			if err := out.Values.AddNode(v, rel.File, rel.Doc, []string{"spec", "values"}); err != nil {
				return nil, err
			}
		} else {
			m, err := decode(v)
			if err != nil {
				return nil, fmt.Errorf("%s: spec.values: %w", out.ID, err)
			}
			label := "HelmRelease " + out.ID + " spec.values"
			out.Values.AddAttributed(m, label, func(p []string) []values.Origin {
				if r.Attribute == nil {
					return nil
				}
				return r.Attribute(rel, p)
			})
		}
	}
	return out, nil
}

func (r *Resolver) chart(rel *Object, spec *yaml.Node, ns string) (source.Ref, error) {
	if cr := Get(spec, "chartRef"); cr != nil {
		kind, name := Str(cr, "kind"), Str(cr, "name")
		crNS := Str(cr, "namespace")
		if crNS == "" {
			crNS = ns
		}
		switch kind {
		case "OCIRepository":
			o := r.find(kind, name, crNS, rel.File)
			if o == nil {
				return source.Ref{}, fmt.Errorf("OCIRepository %s/%s not found in the repository", crNS, name)
			}
			return ociChart(Get(o.Node, "spec"), "OCIRepository "+crNS+"/"+name)
		case "HelmChart":
			o := r.find(kind, name, crNS, rel.File)
			if o == nil {
				return source.Ref{}, fmt.Errorf("HelmChart %s/%s not found in the repository", crNS, name)
			}
			return r.chartSpec(o, Get(o.Node, "spec"), crNS)
		default:
			return source.Ref{}, fmt.Errorf("chartRef kind %q is not supported", kind)
		}
	}
	cs := Get(spec, "chart", "spec")
	if cs == nil {
		return source.Ref{}, fmt.Errorf("no spec.chart or spec.chartRef")
	}
	if Str(spec, "chart", "kind") == "OCIRepository" || Get(cs, "sourceRef") == nil && Str(cs, "url") != "" {
		return ociChart(cs, "the OCIRepository in spec.chart.spec")
	}
	if Get(cs, "sourceRef") == nil {
		return source.Ref{}, fmt.Errorf("spec.chart.spec has neither a sourceRef nor an OCIRepository url")
	}
	return r.chartSpec(rel, cs, ns)
}

// ociChart reads an OCIRepository spec, on its own or inlined in a HelmRelease (Flux
// 2.10): the url, and the tag or semver range of ref, latest without one.
func ociChart(spec *yaml.Node, label string) (source.Ref, error) {
	if Str(spec, "url") == "" {
		return source.Ref{}, fmt.Errorf("%s has no url", label)
	}
	version := Str(spec, "ref", "tag")
	if s := Str(spec, "ref", "semver"); s != "" {
		version = s
	}
	if version == "" && Str(spec, "ref", "digest") != "" {
		return source.Ref{}, fmt.Errorf("%s is pinned by digest only; deadvalues needs a tag or a semver range", label)
	}
	if version == "" {
		version = "latest"
	}
	return source.Ref{Chart: Str(spec, "url"), Version: version}, nil
}

func (r *Resolver) chartSpec(owner *Object, cs *yaml.Node, ns string) (source.Ref, error) {
	chart, version := Str(cs, "chart"), Str(cs, "version")
	kind, name := Str(cs, "sourceRef", "kind"), Str(cs, "sourceRef", "name")
	srcNS := Str(cs, "sourceRef", "namespace")
	if srcNS == "" {
		srcNS = ns
	}
	switch kind {
	case "HelmRepository":
		o := r.find(kind, name, srcNS, owner.File)
		if o == nil {
			return source.Ref{}, fmt.Errorf("HelmRepository %s/%s not found in the repository", srcNS, name)
		}
		url := Str(o.Node, "spec", "url")
		if Str(o.Node, "spec", "type") == "oci" || strings.HasPrefix(url, "oci://") {
			if version == "" {
				version = "*"
			}
			if !strings.HasPrefix(url, "oci://") {
				url = "oci://" + url
			}
		}
		return source.Ref{Repo: url, Chart: chart, Version: version}, nil
	case "GitRepository":
		if o := r.find(kind, name, srcNS, owner.File); o != nil {
			if url := Str(o.Node, "spec", "url"); url != "" && r.Origin != "" && !git.SameRepository(url, r.Origin) {
				return source.Ref{}, &otherRepository{fmt.Sprintf("Helm chart %s from GitRepository %s/%s is in %s, not in this repository; charts from other git repositories are not supported yet", chart, srcNS, name, url)}
			}
		}
		p := filepath.Join(r.RepoRoot, filepath.FromSlash(strings.TrimPrefix(chart, "./")))
		if _, err := os.Stat(filepath.Join(p, "Chart.yaml")); err != nil {
			return source.Ref{}, fmt.Errorf("chart %s from GitRepository %s/%s is not in this repository; charts from other git repositories are not supported", chart, srcNS, name)
		}
		return source.Ref{Chart: p}, nil
	default:
		return source.Ref{}, fmt.Errorf("sourceRef kind %q is not supported", kind)
	}
}

func (r *Resolver) valuesFrom(out *Release, rel *Object, vf *yaml.Node, ns string) error {
	kind, name := Str(vf, "kind"), Str(vf, "name")
	key := Str(vf, "valuesKey")
	if key == "" {
		key = "values.yaml"
	}
	target := Str(vf, "targetPath")
	optional := Str(vf, "optional") == "true"
	label := kind + " " + ns + "/" + name

	o := r.find(kind, name, ns, rel.File)
	if o == nil && optional {
		return nil
	}
	sops := o != nil && kind == "Secret" && Get(o.Node, "sops") != nil
	if o == nil || sops {
		why := "are not in the repository"
		if sops {
			why = "are SOPS-encrypted"
		}
		if target != "" {
			out.Placeholders = append(out.Placeholders, strings.Split(target, "."))
		}
		noteMissing(out, "values from "+label+" "+why, target)
		return nil
	}

	if o.Gen != nil {
		file, ok := o.Gen[key]
		if !ok {
			if optional {
				return nil
			}
			return fmt.Errorf("valuesFrom %s has no key %q", label, key)
		}
		if target != "" {
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			out.Values.AddAt(strings.Split(target, "."), strings.TrimRight(string(data), "\n"), values.Origin{File: file, IsFile: true})
			return nil
		}
		return out.Values.AddFile(file)
	}

	data, ok := dataValue(o, key)
	if !ok {
		if optional {
			return nil
		}
		return fmt.Errorf("valuesFrom %s has no key %q", label, key)
	}
	if target != "" {
		out.Values.AddAt(strings.Split(target, "."), data, values.Origin{File: label})
		return nil
	}
	return out.Values.AddYAML([]byte(data), label)
}

// noteMissing adds one note per missing source, listing every targetPath
// that got a placeholder instead of one note each.
func noteMissing(out *Release, prefix, target string) {
	if target == "" {
		out.Notes = append(out.Notes, prefix+" and were not checked")
		return
	}
	head := prefix + "; placeholders at "
	for i, n := range out.Notes {
		if strings.HasPrefix(n, head) {
			out.Notes[i] = n + ", " + target
			return
		}
	}
	out.Notes = append(out.Notes, head+target)
}

func dataValue(o *Object, key string) (string, bool) {
	if v := Get(o.Node, "stringData", key); v != nil {
		return v.Value, true
	}
	v := Get(o.Node, "data", key)
	if v == nil {
		return "", false
	}
	if o.Kind == "Secret" {
		b, err := base64.StdEncoding.DecodeString(v.Value)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return v.Value, true
}

func decode(n *yaml.Node) (map[string]interface{}, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(values.ResolveAliases(n)); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	vals, err := common.ReadValues(buf.Bytes())
	if err != nil {
		return nil, err
	}
	return vals.AsMap(), nil
}
