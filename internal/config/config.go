// Package config loads .deadvalues.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

const FileName = ".deadvalues.yaml"

type Config struct {
	KubeVersion string   `json:"kubeVersion"`
	APIVersions []string `json:"apiVersions"`
	FailOn      string   `json:"failOn"`
	Ignore      []Ignore `json:"ignore"`
	Flux        Flux     `json:"flux"`
}

// Flux holds values for ${var} placeholders that Flux postBuild
// substitution fills in at reconcile time.
type Flux struct {
	Substitute map[string]string `json:"substitute"`
}

// Ignore accepts keys on purpose. Without App it applies to every app.
// Keys are dotted patterns: `*` matches one segment, `**` any number of
// segments, and a pattern matching a map also covers everything under it.
type Ignore struct {
	App  string   `json:"app"`
	Keys []string `json:"keys"`
}

// Load reads path. A missing file is an error only when required is true.
func Load(p string, required bool) (*Config, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) && !required {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return &c, nil
}

func Default(repoRoot string) string { return filepath.Join(repoRoot, FileName) }

func (c *Config) Ignored(app string, key []string) bool {
	for _, ig := range c.Ignore {
		if ig.App != "" && ig.App != app {
			continue
		}
		for _, pattern := range ig.Keys {
			if Match(pattern, key) {
				return true
			}
		}
	}
	return false
}

// Match reports whether pattern matches key or one of its ancestors.
func Match(pattern string, key []string) bool {
	pat := strings.Split(pattern, ".")
	for i := 1; i <= len(key); i++ {
		if match(pat, key[:i]) {
			return true
		}
	}
	return false
}

func match(pat, key []string) bool {
	if len(pat) == 0 {
		return len(key) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(key); i++ {
			if match(pat[1:], key[i:]) {
				return true
			}
		}
		return false
	}
	if len(key) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], key[0])
	if err != nil || !ok {
		return false
	}
	return match(pat[1:], key[1:])
}
