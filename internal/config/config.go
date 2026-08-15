// Package config loads .stamp.yml, or derives an equivalent configuration by
// looking at the repository when there is no config file.
//
// The config file is optional on purpose: in a repository that keeps a VERSION
// file and releases v-prefixed tags from main — which is the common case —
// `stamp release minor` has to work with no setup at all.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/p-arndt/stamp/internal/source"
	"github.com/p-arndt/stamp/internal/version"
	"gopkg.in/yaml.v3"
)

// FileName is the config file stamp looks for in the repository root.
const FileName = ".stamp.yml"

// Defaults applied wherever the config is silent.
const (
	DefaultBranch    = "main"
	DefaultRemote    = "origin"
	DefaultTag       = "v{{version}}"
	DefaultCommit    = "release: {{tag}}"
	DefaultVersion   = "VERSION"
	DefaultPackage   = "package.json"
	defaultJSONField = "version"
)

// Config is the resolved release configuration: what the file said, filled in
// with defaults and detection.
type Config struct {
	// ProjectName is used only in output. Empty means "the directory name".
	ProjectName string
	// Source is the authoritative version location.
	Source source.Source
	// Mirrors are further locations kept in sync with Source.
	Mirrors []source.Source
	// Branch releases must be cut from.
	Branch string
	// Remote to push to.
	Remote string
	// TagTemplate and CommitTemplate support {{version}} and {{tag}}.
	TagTemplate    string
	CommitTemplate string
	// Push reports whether stamp pushes by default.
	Push bool
	// PreID is the default pre-release identifier for `stamp prerelease`.
	PreID string
	// FromFile records whether a .stamp.yml was found, for the plan output.
	FromFile bool
}

// file mirrors the YAML shape one-to-one. It is kept separate from Config so
// the YAML can stay declarative (strings, no interfaces) while Config holds
// resolved objects.
type file struct {
	Project struct {
		Name string `yaml:"name"`
	} `yaml:"project"`
	Version struct {
		Source  *sourceSpec  `yaml:"source"`
		Mirrors []sourceSpec `yaml:"mirrors"`
	} `yaml:"version"`
	Release struct {
		Branch     string `yaml:"branch"`
		Remote     string `yaml:"remote"`
		Tag        string `yaml:"tag"`
		Commit     string `yaml:"commit"`
		Push       *bool  `yaml:"push"`
		Prerelease string `yaml:"prerelease"`
	} `yaml:"release"`
}

type sourceSpec struct {
	Type  string `yaml:"type"`
	Path  string `yaml:"path"`
	Field string `yaml:"field"`
}

// Load reads root/.stamp.yml if present and otherwise detects the layout.
func Load(root string) (*Config, error) {
	cfg := &Config{
		ProjectName:    filepath.Base(root),
		Branch:         DefaultBranch,
		Remote:         DefaultRemote,
		TagTemplate:    DefaultTag,
		CommitTemplate: DefaultCommit,
		Push:           true,
		PreID:          version.DefaultPreID,
	}

	raw, err := os.ReadFile(filepath.Join(root, FileName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		src, derr := detect(root)
		if derr != nil {
			return nil, derr
		}
		cfg.Source = src
		return cfg, nil
	case err != nil:
		return nil, err
	}

	var f file
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a typo in the config must not be silently ignored
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}

	cfg.FromFile = true
	if f.Project.Name != "" {
		cfg.ProjectName = f.Project.Name
	}
	if f.Release.Branch != "" {
		cfg.Branch = f.Release.Branch
	}
	if f.Release.Remote != "" {
		cfg.Remote = f.Release.Remote
	}
	if f.Release.Tag != "" {
		cfg.TagTemplate = f.Release.Tag
	}
	if f.Release.Commit != "" {
		cfg.CommitTemplate = f.Release.Commit
	}
	if f.Release.Push != nil {
		cfg.Push = *f.Release.Push
	}
	if f.Release.Prerelease != "" {
		cfg.PreID = f.Release.Prerelease
	}

	if f.Version.Source == nil {
		// A config that configures the release but not the version is fine —
		// fall back to detection for the version location.
		src, derr := detect(root)
		if derr != nil {
			return nil, fmt.Errorf("%s has no version.source and %w", FileName, derr)
		}
		cfg.Source = src
	} else {
		src, err := build(root, *f.Version.Source)
		if err != nil {
			return nil, fmt.Errorf("%s: version.source: %w", FileName, err)
		}
		cfg.Source = src
	}

	seen := map[string]bool{cfg.Source.Path(): true}
	for i, spec := range f.Version.Mirrors {
		m, err := build(root, spec)
		if err != nil {
			return nil, fmt.Errorf("%s: version.mirrors[%d]: %w", FileName, i, err)
		}
		if seen[m.Path()] {
			return nil, fmt.Errorf("%s: version.mirrors[%d]: %s is already listed", FileName, i, m.Path())
		}
		seen[m.Path()] = true
		cfg.Mirrors = append(cfg.Mirrors, m)
	}

	if err := validateTemplate(cfg.TagTemplate, "release.tag", "version"); err != nil {
		return nil, err
	}
	if err := validateTemplate(cfg.CommitTemplate, "release.commit", "version", "tag"); err != nil {
		return nil, err
	}
	return cfg, nil
}

func build(root string, s sourceSpec) (source.Source, error) {
	if s.Type == "" {
		return nil, errors.New("type is required (file or json)")
	}
	return source.New(root, s.Type, s.Path, s.Field)
}

// detect picks the version location from what is in the repository root:
// a VERSION file wins over a package.json, because a project with both keeps
// the plain file as the source of truth and mirrors it into package.json.
func detect(root string) (source.Source, error) {
	if exists(filepath.Join(root, DefaultVersion)) {
		return source.New(root, source.KindFile, DefaultVersion, "")
	}
	if exists(filepath.Join(root, DefaultPackage)) {
		return source.New(root, source.KindJSON, DefaultPackage, defaultJSONField)
	}
	return nil, fmt.Errorf("no version source found: expected a %s file or a %s in %s — create one or describe it in %s",
		DefaultVersion, DefaultPackage, root, FileName)
}

// AllSources returns the source followed by its mirrors.
func (c *Config) AllSources() []source.Source {
	return append([]source.Source{c.Source}, c.Mirrors...)
}

// Paths returns the repository-relative path of every version location.
func (c *Config) Paths() []string {
	all := c.AllSources()
	paths := make([]string, len(all))
	for i, s := range all {
		paths[i] = s.Path()
	}
	return paths
}

// RenderTag expands the tag template for version.
func (c *Config) RenderTag(version string) string {
	return expand(c.TagTemplate, map[string]string{"version": version})
}

// RenderCommit expands the commit message template.
func (c *Config) RenderCommit(version, tag string) string {
	return expand(c.CommitTemplate, map[string]string{"version": version, "tag": tag})
}

// expand replaces {{ key }} placeholders, tolerating any inner whitespace.
func expand(tmpl string, vars map[string]string) string {
	out := tmpl
	for key, value := range vars {
		for _, form := range []string{"{{" + key + "}}", "{{ " + key + " }}"} {
			out = strings.ReplaceAll(out, form, value)
		}
	}
	return out
}

// validateTemplate rejects templates with unknown or missing placeholders, so a
// typo surfaces as a config error instead of a tag literally named
// "v{{ vesion }}".
func validateTemplate(tmpl, field string, allowed ...string) error {
	rest := tmpl
	used := false
	for {
		open := strings.Index(rest, "{{")
		if open < 0 {
			break
		}
		end := strings.Index(rest[open:], "}}")
		if end < 0 {
			return fmt.Errorf("%s: unterminated %q in %q", field, "{{", tmpl)
		}
		name := strings.TrimSpace(rest[open+2 : open+end])
		if !contains(allowed, name) {
			return fmt.Errorf("%s: unknown placeholder {{ %s }} in %q (allowed: %s)",
				field, name, tmpl, strings.Join(allowed, ", "))
		}
		used = true
		rest = rest[open+end+2:]
	}
	if !used {
		return fmt.Errorf("%s: %q contains no placeholder — every release would use the same value", field, tmpl)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
