// Package config loads .stamp.yml, or derives an equivalent configuration by
// looking at the repository when there is no config file.
//
// The config file is optional on purpose: in a repository that keeps a VERSION
// file and releases v-prefixed tags from main, which is the common case,
// `stamp release minor` has to work with no setup at all.
//
// A repository may hold more than one thing that is versioned and released on
// its own. Those are components: each has its own version locations and its own
// tag, and each is released by name. Everything under `release:` is the shared
// baseline, and a component may override any of it key by key. A repository
// with a single version writes no `components:` block at all and behaves
// exactly as it always did.
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
	DefaultBranch  = "main"
	DefaultRemote  = "origin"
	DefaultTag     = "v{{version}}"
	DefaultCommit  = "release: {{tag}}"
	DefaultVersion = "VERSION"
	DefaultPackage = "package.json"

	DefaultChangelogFile     = "CHANGELOG.md"
	DefaultChangelogDir      = ".stamp/changelog"
	DefaultChangelogFallback = "commits"
)

// Fallback values `changelog.fallback` accepts.
const (
	FallbackCommits = "commits"
	FallbackNone    = "none"
)

// Config is the resolved release configuration: what the file said, filled in
// with defaults and detection.
type Config struct {
	// ProjectName is used in output. Empty means "the directory name".
	ProjectName string
	// Components always holds at least one entry. A repository without a
	// `components:` block has exactly one, with an empty Name.
	Components []*Component
	// Multi reports whether the config declared components, and so whether a
	// command has to be told which one it means.
	Multi bool
	// FromFile records whether a .stamp.yml was found, for the plan output.
	FromFile bool
	// Legacy records that the file used the superseded version.source /
	// version.mirrors shape, so the CLI can say so once.
	Legacy bool
}

// Component is one independently versioned, independently tagged unit.
type Component struct {
	// Name is empty for the implicit single component of a plain repository.
	Name string
	// project is the project name, used when Name is empty.
	project string
	// Sources are the places this component's version is stored. The first is
	// the source of truth; the rest are kept in step with it.
	Sources []source.Source

	Branch string
	Remote string
	// TagTemplate and CommitTemplate support {{version}}, {{component}} and,
	// in the commit, {{tag}}.
	TagTemplate    string
	CommitTemplate string
	Push           bool
	// PreID is the default pre-release identifier for `stamp prerelease`.
	PreID string
	// Changelog is this component's news-fragment configuration.
	Changelog Changelog
}

// Changelog is where this component's changelog entries are collected and
// where the rendered file lives.
type Changelog struct {
	// File is the repository-relative changelog file. Empty means stamp
	// renders no file and only carries the entries into the tag.
	File string
	// Dir is the repository-relative directory the fragments are written to.
	Dir string
	// Fallback names what a release without fragments falls back to:
	// "commits" drafts entries from the conventional commits since the last
	// tag, "none" leaves the section empty.
	Fallback string
	// Require makes an empty changelog a failed preflight check.
	Require bool
	// TagBody puts the rendered section into the annotated tag message.
	TagBody bool
	// Declared records that a changelog: block named any of this, which is
	// what makes the feature opt-in: see Component.ChangelogEnabled.
	Declared bool
}

// ChangelogEnabled reports whether this release should touch the changelog at
// all. The feature is opt-in by use rather than by a flag: a repository that
// has never run `stamp note`, has no changelog file and says nothing about one
// in its config releases exactly as it did before the feature existed.
// root is the absolute repository root.
func (c *Component) ChangelogEnabled(root string) bool {
	if c.Changelog.Declared {
		return true
	}
	if c.Changelog.Dir != "" && isDir(filepath.Join(root, filepath.FromSlash(c.Changelog.Dir))) {
		return true
	}
	return c.Changelog.File != "" && exists(filepath.Join(root, filepath.FromSlash(c.Changelog.File)))
}

// Source is the authoritative version location.
func (c *Component) Source() source.Source { return c.Sources[0] }

// Mirrors are the further locations kept in sync with the source.
func (c *Component) Mirrors() []source.Source { return c.Sources[1:] }

// Paths returns the repository-relative path of every version location.
func (c *Component) Paths() []string {
	paths := make([]string, len(c.Sources))
	for i, s := range c.Sources {
		paths[i] = s.Path()
	}
	return paths
}

// Label names the component in output: its own name, or the project's when it
// is the only one.
func (c *Component) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.project
}

// RenderTag expands the tag template for version.
func (c *Component) RenderTag(v string) string {
	return expand(c.TagTemplate, map[string]string{"version": v, "component": c.Name})
}

// RenderCommit expands the commit message template.
func (c *Component) RenderCommit(v, tag string) string {
	return expand(c.CommitTemplate, map[string]string{"version": v, "tag": tag, "component": c.Name})
}

// Lookup returns the component of that name, or nil.
func (c *Config) Lookup(name string) *Component {
	for _, comp := range c.Components {
		if comp.Name == name {
			return comp
		}
	}
	return nil
}

// Names lists the declared component names, in the order the file wrote them.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Components))
	for _, comp := range c.Components {
		if comp.Name != "" {
			names = append(names, comp.Name)
		}
	}
	return names
}

// Only returns the single component of a repository that declares none.
func (c *Config) Only() *Component { return c.Components[0] }

// Load reads root/.stamp.yml if present and otherwise detects the layout.
func Load(root string) (*Config, error) {
	raw, err := os.ReadFile(filepath.Join(root, FileName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return detected(root)
	case err != nil:
		return nil, err
	}
	return parse(root, raw)
}

// detected is the configuration of a repository with no .stamp.yml.
func detected(root string) (*Config, error) {
	spec, err := detectSpec(root)
	if err != nil {
		return nil, err
	}
	src, err := source.New(root, spec)
	if err != nil {
		return nil, err
	}
	comp := baseComponent(filepath.Base(root))
	comp.Sources = []source.Source{src}
	return &Config{
		ProjectName: filepath.Base(root),
		Components:  []*Component{comp},
	}, nil
}

// baseComponent is a component with nothing but the defaults in it.
func baseComponent(project string) *Component {
	return &Component{
		project:        project,
		Branch:         DefaultBranch,
		Remote:         DefaultRemote,
		TagTemplate:    DefaultTag,
		CommitTemplate: DefaultCommit,
		Push:           true,
		PreID:          version.DefaultPreID,
		Changelog: Changelog{
			File:     DefaultChangelogFile,
			Dir:      DefaultChangelogDir,
			Fallback: DefaultChangelogFallback,
			Require:  false,
			TagBody:  true,
		},
	}
}

// parse turns the bytes of a .stamp.yml into a Config. It is separate from
// Load so `stamp init` can validate the file it is about to write by loading
// it through exactly this path.
//
// The file is walked as a node tree rather than decoded into a struct, because
// two of its keys accept more than one shape (`version:` is a list, a single
// entry, or the superseded source/mirrors mapping), and because a node carries
// the line number that makes a config error findable.
func parse(root string, raw []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s is empty, delete it or run `stamp init`", FileName)
	}
	top := doc.Content[0]
	if err := strictKeys(top, "top level", "project", "version", "release", "changelog", "components"); err != nil {
		return nil, err
	}

	cfg := &Config{ProjectName: filepath.Base(root), FromFile: true}
	if n := field(top, "project"); n != nil {
		name, err := parseProject(n)
		if err != nil {
			return nil, err
		}
		if name != "" {
			cfg.ProjectName = name
		}
	}

	base := baseComponent(cfg.ProjectName)
	if n := field(top, "release"); n != nil {
		if err := strictKeys(n, "release", releaseKeys...); err != nil {
			return nil, err
		}
		spec, err := parseRelease(n, "release")
		if err != nil {
			return nil, err
		}
		spec.applyTo(base)
	}
	if n := field(top, "changelog"); n != nil {
		spec, err := parseChangelog(n, "changelog")
		if err != nil {
			return nil, err
		}
		spec.applyTo(base)
	}

	versionNode := field(top, "version")
	componentsNode := field(top, "components")

	switch {
	case componentsNode != nil:
		if versionNode != nil {
			return nil, fmt.Errorf("%s:%d: a repository with components keeps its version locations inside each component, not at the top level",
				FileName, versionNode.Line)
		}
		cfg.Multi = true
		comps, err := parseComponents(root, componentsNode, base)
		if err != nil {
			return nil, err
		}
		cfg.Components = comps
	default:
		comp := *base
		specs, legacy, err := versionSpecs(versionNode, root)
		if err != nil {
			return nil, err
		}
		cfg.Legacy = legacy
		if err := bind(root, &comp, specs, "version"); err != nil {
			return nil, err
		}
		cfg.Components = []*Component{&comp}
	}

	return cfg, validate(cfg)
}

// versionSpecs resolves the top-level `version:` key, falling back to detection
// when the file configures the release but not the version.
func versionSpecs(n *yaml.Node, root string) (specs []source.Spec, legacy bool, err error) {
	if n == nil {
		spec, derr := detectSpec(root)
		if derr != nil {
			return nil, false, fmt.Errorf("%s lists no version locations and %w", FileName, derr)
		}
		return []source.Spec{spec}, false, nil
	}
	specs, legacy, err = parseVersion(n, "version")
	return specs, legacy, err
}

// parseProject accepts both `project: name` and the superseded
// `project: {name: …}` mapping.
func parseProject(n *yaml.Node) (string, error) {
	if n.Kind == yaml.ScalarNode {
		return n.Value, nil
	}
	if err := strictKeys(n, "project", "name"); err != nil {
		return "", err
	}
	if name := field(n, "name"); name != nil {
		return name.Value, nil
	}
	return "", nil
}

// parseVersion accepts every shape a version list may take: one location, a
// list of them, or the superseded source/mirrors mapping.
func parseVersion(n *yaml.Node, where string) (specs []source.Spec, legacy bool, err error) {
	switch n.Kind {
	case yaml.ScalarNode:
		spec, err := entrySpec(n, where)
		if err != nil {
			return nil, false, err
		}
		return []source.Spec{spec}, false, nil

	case yaml.SequenceNode:
		for i, item := range n.Content {
			spec, err := entrySpec(item, fmt.Sprintf("%s[%d]", where, i))
			if err != nil {
				return nil, false, err
			}
			specs = append(specs, spec)
		}
		if len(specs) == 0 {
			return nil, false, fmt.Errorf("%s:%d: %s is an empty list, it must name at least one file", FileName, n.Line, where)
		}
		return specs, false, nil

	case yaml.MappingNode:
		// A single written-out location, or the superseded shape.
		if field(n, "source") != nil || field(n, "mirrors") != nil {
			specs, err := parseLegacyVersion(n, where)
			return specs, true, err
		}
		spec, err := entrySpec(n, where)
		if err != nil {
			return nil, false, err
		}
		return []source.Spec{spec}, false, nil
	}
	return nil, false, fmt.Errorf("%s:%d: %s must be a file, or a list of files", FileName, n.Line, where)
}

// parseLegacyVersion reads the superseded `source:` / `mirrors:` shape. It is
// still accepted so an existing config keeps working; the CLI prints a notice
// pointing at the list form.
func parseLegacyVersion(n *yaml.Node, where string) ([]source.Spec, error) {
	if err := strictKeys(n, where, "source", "mirrors"); err != nil {
		return nil, err
	}
	var specs []source.Spec
	src := field(n, "source")
	if src == nil {
		return nil, fmt.Errorf("%s:%d: %s.mirrors without a %s.source: the first location is the one the rest follow", FileName, n.Line, where, where)
	}
	spec, err := entrySpec(src, where+".source")
	if err != nil {
		return nil, err
	}
	specs = append(specs, spec)

	if mirrors := field(n, "mirrors"); mirrors != nil {
		if mirrors.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("%s:%d: %s.mirrors must be a list", FileName, mirrors.Line, where)
		}
		for i, item := range mirrors.Content {
			spec, err := entrySpec(item, fmt.Sprintf("%s.mirrors[%d]", where, i))
			if err != nil {
				return nil, err
			}
			specs = append(specs, spec)
		}
	}
	return specs, nil
}

// entrySpec reads one version location, in either the "path#field" shorthand or
// the written-out mapping.
func entrySpec(n *yaml.Node, where string) (source.Spec, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		spec, err := source.ParseSpec(n.Value)
		if err != nil {
			return source.Spec{}, fmt.Errorf("%s:%d: %s: %w", FileName, n.Line, where, err)
		}
		return spec, nil
	case yaml.MappingNode:
		if err := strictKeys(n, where, "path", "type", "field"); err != nil {
			return source.Spec{}, err
		}
		var spec source.Spec
		if err := n.Decode(&spec); err != nil {
			return source.Spec{}, fmt.Errorf("%s:%d: %s: %w", FileName, n.Line, where, err)
		}
		if spec.Path == "" {
			return source.Spec{}, fmt.Errorf("%s:%d: %s: path is required", FileName, n.Line, where)
		}
		return spec, nil
	}
	return source.Spec{}, fmt.Errorf("%s:%d: %s must be a file such as %q, or a mapping with a path",
		FileName, n.Line, where, "package.json#version")
}

// releaseSpec is the set of release keys, as written in `release:` or inside a
// component. Every field is a pointer or an empty string so "not written" stays
// distinguishable from "written as the default", which is what makes a
// component override exactly the keys it names.
type releaseSpec struct {
	Branch     string `yaml:"branch"`
	Remote     string `yaml:"remote"`
	Tag        string `yaml:"tag"`
	Commit     string `yaml:"commit"`
	Prerelease string `yaml:"prerelease"`
	Push       *bool  `yaml:"push"`
}

// releaseKeys are the keys a component may override, and the keys `release:`
// accepts.
var releaseKeys = []string{"branch", "remote", "tag", "commit", "prerelease", "push"}

// parseRelease reads the release keys out of a mapping. The caller has already
// checked which keys the mapping may hold (a component's may also hold
// `version:`), so this only decodes.
func parseRelease(n *yaml.Node, where string) (releaseSpec, error) {
	var spec releaseSpec
	if n.Kind != yaml.MappingNode {
		return spec, fmt.Errorf("%s:%d: %s must be a mapping", FileName, n.Line, where)
	}
	if err := n.Decode(&spec); err != nil {
		return spec, fmt.Errorf("%s:%d: %s: %w", FileName, n.Line, where, err)
	}
	return spec, nil
}

func (r releaseSpec) applyTo(c *Component) {
	if r.Branch != "" {
		c.Branch = r.Branch
	}
	if r.Remote != "" {
		c.Remote = r.Remote
	}
	if r.Tag != "" {
		c.TagTemplate = r.Tag
	}
	if r.Commit != "" {
		c.CommitTemplate = r.Commit
	}
	if r.Prerelease != "" {
		c.PreID = r.Prerelease
	}
	if r.Push != nil {
		c.Push = *r.Push
	}
}

// changelogSpec is the set of changelog keys, as written in `changelog:` or
// inside a component. Every field is a pointer or an empty string so "not
// written" stays distinguishable from "written as the default", which is what
// makes a component override exactly the keys it names.
type changelogSpec struct {
	File     *string `yaml:"file"`
	Dir      *string `yaml:"dir"`
	Fallback string  `yaml:"fallback"`
	Require  *bool   `yaml:"require"`
	TagBody  *bool   `yaml:"tag_body"`
}

// changelogKeys are the keys `changelog:` accepts, at the top level and inside
// a component.
var changelogKeys = []string{"file", "dir", "fallback", "require", "tag_body"}

// parseChangelog reads the `changelog:` mapping, rejecting the values that
// cannot mean anything before they reach a release.
func parseChangelog(n *yaml.Node, where string) (changelogSpec, error) {
	var spec changelogSpec
	if n.Kind != yaml.MappingNode {
		return spec, fmt.Errorf("%s:%d: %s must be a mapping", FileName, n.Line, where)
	}
	if err := strictKeys(n, where, changelogKeys...); err != nil {
		return spec, err
	}
	if err := n.Decode(&spec); err != nil {
		return spec, fmt.Errorf("%s:%d: %s: %w", FileName, n.Line, where, err)
	}
	if spec.Fallback != "" && spec.Fallback != FallbackCommits && spec.Fallback != FallbackNone {
		return spec, fmt.Errorf("%s:%d: %s.fallback is %q; it must be %q or %q",
			FileName, n.Line, where, spec.Fallback, FallbackCommits, FallbackNone)
	}
	// An empty file: is legal and means "render no file"; an empty dir: is not,
	// because the fragments would then land in the repository root.
	if spec.File != nil && *spec.File != "" {
		if err := checkInsideRepo(*spec.File, where+".file", n.Line); err != nil {
			return spec, err
		}
	}
	if spec.Dir != nil {
		if *spec.Dir == "" {
			return spec, fmt.Errorf("%s:%d: %s.dir is empty; name the directory the changelog entries are collected in", FileName, n.Line, where)
		}
		if err := checkInsideRepo(*spec.Dir, where+".dir", n.Line); err != nil {
			return spec, err
		}
	}
	return spec, nil
}

// checkInsideRepo rejects any path that could resolve outside the repository.
// Both the unix and the Windows form of an absolute path are rejected on every
// platform, because a config file travels between machines.
func checkInsideRepo(path, where string, line int) error {
	bad := func(why string) error {
		return fmt.Errorf("%s:%d: %s is %q, which %s; it must be relative to the repository root",
			FileName, line, where, path, why)
	}
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return bad("is absolute")
	}
	if len(path) >= 2 && path[1] == ':' {
		return bad("names a drive")
	}
	for _, segment := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return bad(`walks up out of the repository ("..")`)
		}
	}
	return nil
}

func (s changelogSpec) applyTo(c *Component) {
	c.Changelog.Declared = true
	if s.File != nil {
		c.Changelog.File = *s.File
	}
	if s.Dir != nil {
		c.Changelog.Dir = *s.Dir
	}
	if s.Fallback != "" {
		c.Changelog.Fallback = s.Fallback
	}
	if s.Require != nil {
		c.Changelog.Require = *s.Require
	}
	if s.TagBody != nil {
		c.Changelog.TagBody = *s.TagBody
	}
}

// parseComponents reads the `components:` mapping, each entry inheriting base.
func parseComponents(root string, n *yaml.Node, base *Component) ([]*Component, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s:%d: components must be a mapping of name to component", FileName, n.Line)
	}
	if len(n.Content) == 0 {
		return nil, fmt.Errorf("%s:%d: components is empty, remove it or name at least one", FileName, n.Line)
	}

	var comps []*Component
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		keyNode, valNode := n.Content[i], n.Content[i+1]
		name := keyNode.Value
		if err := checkComponentName(name, keyNode.Line); err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("%s:%d: component %q is declared twice", FileName, keyNode.Line, name)
		}
		seen[name] = true

		where := "components." + name
		if valNode.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s:%d: %s must be a mapping", FileName, valNode.Line, where)
		}
		if err := strictKeys(valNode, where, append([]string{"version", "changelog"}, releaseKeys...)...); err != nil {
			return nil, err
		}

		comp := *base
		comp.Name = name
		spec, err := parseRelease(valNode, where)
		if err != nil {
			return nil, err
		}
		spec.applyTo(&comp)

		if cn := field(valNode, "changelog"); cn != nil {
			clSpec, err := parseChangelog(cn, where+".changelog")
			if err != nil {
				return nil, err
			}
			clSpec.applyTo(&comp)
		}

		versionNode := field(valNode, "version")
		if versionNode == nil {
			return nil, fmt.Errorf("%s:%d: %s has no version: name the file its version lives in", FileName, valNode.Line, where)
		}
		specs, _, err := parseVersion(versionNode, where+".version")
		if err != nil {
			return nil, err
		}
		if err := bind(root, &comp, specs, where+".version"); err != nil {
			return nil, err
		}
		comps = append(comps, &comp)
	}
	return comps, nil
}

// bind turns specs into sources on the component, rejecting a file listed twice.
func bind(root string, c *Component, specs []source.Spec, where string) error {
	seen := map[string]bool{}
	for i, spec := range specs {
		src, err := source.New(root, spec)
		if err != nil {
			return fmt.Errorf("%s: %s[%d]: %w", FileName, where, i, err)
		}
		if seen[src.Path()] {
			return fmt.Errorf("%s: %s[%d]: %s is already listed", FileName, where, i, src.Path())
		}
		seen[src.Path()] = true
		c.Sources = append(c.Sources, src)
	}
	if len(c.Sources) == 0 {
		return fmt.Errorf("%s: %s names no file", FileName, where)
	}
	return nil
}

// bumpKeywords may not be used as component names: `stamp release minor` has to
// stay unambiguous.
var bumpKeywords = []string{"patch", "minor", "major", "final"}

func checkComponentName(name string, line int) error {
	if name == "" {
		return fmt.Errorf("%s:%d: a component needs a name", FileName, line)
	}
	for _, kw := range bumpKeywords {
		if name == kw {
			return fmt.Errorf("%s:%d: %q cannot be a component name: it is a bump keyword, and `stamp release %s` would be ambiguous", FileName, line, name, name)
		}
	}
	for _, r := range name {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("%s:%d: component name %q may only hold lowercase letters, digits and dashes, because it is typed on the command line and rendered into tags", FileName, line, name)
		}
	}
	return nil
}

// validate runs the checks that need the whole config: template placeholders,
// files claimed by two components, and tag templates that cannot tell two
// components apart.
func validate(cfg *Config) error {
	tagAllowed := []string{"version"}
	commitAllowed := []string{"version", "tag"}
	if cfg.Multi {
		tagAllowed = append(tagAllowed, "component")
		commitAllowed = append(commitAllowed, "component")
	}

	owner := map[string]string{}
	tags := map[string]string{}
	changelogs := map[string]string{}
	for _, comp := range cfg.Components {
		where := "release"
		if comp.Name != "" {
			where = "components." + comp.Name
		}
		if err := validateTemplate(comp.TagTemplate, where+".tag", "version", tagAllowed...); err != nil {
			return err
		}
		if err := validateTemplate(comp.CommitTemplate, where+".commit", "", commitAllowed...); err != nil {
			return err
		}

		for _, src := range comp.Sources {
			if other, taken := owner[src.Path()]; taken && cfg.Multi {
				return fmt.Errorf("%s: %s is listed by both %s and %s: one file cannot hold two versions",
					FileName, src.Path(), other, comp.Name)
			}
			owner[src.Path()] = comp.Name
		}

		if !cfg.Multi {
			continue
		}
		// Two components rendering into one changelog file would overwrite each
		// other's section. Sharing a fragment directory is fine: the fragments
		// live in a per-component subdirectory of it. Only a declared changelog
		// is checked: components that never mention one carry the same default
		// file, and the feature stays off for them anyway.
		if file := comp.Changelog.File; file != "" && comp.Changelog.Declared {
			if other, taken := changelogs[file]; taken {
				return fmt.Errorf("%s: %s and %s would both render into %s: give each component its own changelog file",
					FileName, other, comp.Name, file)
			}
			changelogs[file] = comp.Name
		}
		// Two components whose tag templates come out identical would always
		// want the same tag, and that is only ever found out at the second
		// release. Expanding {{component}}, the one placeholder that is
		// already known here, is what tells the two cases apart.
		key := expand(comp.TagTemplate, map[string]string{"component": comp.Name})
		if other, taken := tags[key]; taken {
			return fmt.Errorf("%s: %s and %s would both tag %q: add {{component}} to the shared template, or give them different prefixes",
				FileName, other, comp.Name, key)
		}
		tags[key] = comp.Name
	}
	return nil
}

// detectSpec picks the version location from what is in the repository root:
// a VERSION file wins over a package.json, because a project with both keeps
// the plain file as the source of truth and mirrors it into package.json.
func detectSpec(root string) (source.Spec, error) {
	if exists(filepath.Join(root, DefaultVersion)) {
		return source.Spec{Path: DefaultVersion}, nil
	}
	if exists(filepath.Join(root, DefaultPackage)) {
		return source.Spec{Path: DefaultPackage, Field: source.DefaultField}, nil
	}
	return source.Spec{}, fmt.Errorf("no version file found: expected a %s file or a %s in %s: create one or run `stamp init`",
		DefaultVersion, DefaultPackage, root)
}

// field returns the value node for key in a mapping node, or nil.
func field(n *yaml.Node, key string) *yaml.Node {
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

// strictKeys rejects a key the schema does not define, so a typo surfaces as a
// config error rather than as a silently ignored line.
func strictKeys(n *yaml.Node, where string, allowed ...string) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("%s:%d: %s must be a mapping", FileName, n.Line, where)
	}
	for i := 0; i < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if !contains(allowed, key) {
			return fmt.Errorf("%s:%d: unknown key %q in %s (allowed: %s)",
				FileName, n.Content[i].Line, key, where, strings.Join(allowed, ", "))
		}
	}
	return nil
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

// validateTemplate rejects templates with unknown placeholders, and templates
// that would render the same string for every release. required, when set, is
// the placeholder without which the template cannot distinguish two versions;
// when it is empty, any one of allowed will do.
func validateTemplate(tmpl, where, required string, allowed ...string) error {
	rest := tmpl
	used := map[string]bool{}
	for {
		open := strings.Index(rest, "{{")
		if open < 0 {
			break
		}
		end := strings.Index(rest[open:], "}}")
		if end < 0 {
			return fmt.Errorf("%s: %s: unterminated %q in %q", FileName, where, "{{", tmpl)
		}
		name := strings.TrimSpace(rest[open+2 : open+end])
		if !contains(allowed, name) {
			return fmt.Errorf("%s: %s: unknown placeholder {{ %s }} in %q (allowed: %s)",
				FileName, where, name, tmpl, placeholderList(allowed))
		}
		used[name] = true
		rest = rest[open+end+2:]
	}

	if required != "" {
		if !used[required] {
			return fmt.Errorf("%s: %s: %q does not contain {{ %s }}, so every release would want the same %s",
				FileName, where, tmpl, required, strings.TrimPrefix(where[strings.LastIndex(where, ".")+1:], "."))
		}
		return nil
	}
	for _, name := range allowed {
		if used[name] {
			return nil
		}
	}
	return fmt.Errorf("%s: %s: %q contains no placeholder, so every release would use the same value", FileName, where, tmpl)
}

func placeholderList(allowed []string) string {
	out := make([]string, len(allowed))
	for i, a := range allowed {
		out[i] = "{{ " + a + " }}"
	}
	return strings.Join(out, ", ")
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

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
