// Package config loads .deadvalues.yaml.
package config

import (
	"encoding/json"
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
	FailOn      Level    `json:"failOn"`
	FailOnError *bool    `json:"failOnError"`
	// Skip adds to --skip: apps by name, or directories relative to the file.
	Skip   []string `json:"skip"`
	Ignore []Ignore `json:"ignore"`
	Flux   Flux     `json:"flux"`
}

// Flux holds values for ${var} placeholders that Flux postBuild
// substitution fills in at reconcile time.
type Flux struct {
	Substitute map[string]string `json:"substitute"`
}

// Ignore accepts keys on purpose, in the apps named App, those of Chart, or every app when both
// are empty. Keys are dotted patterns: `*` is one segment, `**` any number, a map its keys.
type Ignore struct {
	App   string   `json:"app"`
	Chart string   `json:"chart"`
	Keys  []string `json:"keys"`
}

// Level is a failOn level; YAML false, unquoted, reads as none.
type Level string

func (l *Level) UnmarshalJSON(data []byte) error {
	if string(data) == "false" {
		*l = "none"
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("failOn must be a level or false, got %s", data)
	}
	*l = Level(s)
	return nil
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

// Ignored reports whether an ignore entry covers key in app, whose chart is named chart.
func (c *Config) Ignored(app, chart string, key []string) bool {
	for _, ig := range c.Ignore {
		if (ig.App != "" && ig.App != app) || (ig.Chart != "" && ig.Chart != chart) {
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

// Match reports whether pattern matches key or one of its ancestors. A pattern without wildcards
// also matches the key as reports print it, so a key with dots, as a label name, can be copied.
func Match(pattern string, key []string) bool {
	plain := !strings.ContainsAny(pattern, `*?[\`)
	pat := splitPattern(pattern)
	for i := 1; i <= len(key); i++ {
		if (plain && strings.Join(key[:i], ".") == pattern) || match(pat, key[:i]) {
			return true
		}
	}
	return false
}

// splitPattern splits pattern at the dots no backslash escapes; path.Match then reads `\.` as a
// dot inside a segment.
func splitPattern(pattern string) []string {
	var out []string
	start := 0
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
		case '.':
			out = append(out, pattern[start:i])
			start = i + 1
		}
	}
	return append(out, pattern[start:])
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
