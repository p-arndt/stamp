package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// load writes a .stamp.yml and reads it back the way stamp would.
func load(t *testing.T, dir, yaml string) *Config {
	t.Helper()
	write(t, dir, FileName, yaml)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, yaml)
	}
	return cfg
}

func loadErr(t *testing.T, dir, yaml string) error {
	t.Helper()
	write(t, dir, FileName, yaml)
	_, err := Load(dir)
	return err
}

// Without a config file, a VERSION-file repo must work as-is: main, v-prefixed
// tag, origin.
func TestDetectVersionFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FromFile {
		t.Error("FromFile should be false when there is no .stamp.yml")
	}
	if cfg.Multi {
		t.Error("a detected repository has no components")
	}
	comp := cfg.Only()
	if comp.Source().Path() != "VERSION" {
		t.Errorf("source = %q, want VERSION", comp.Source().Path())
	}
	if comp.Branch != "main" || comp.Remote != "origin" || !comp.Push {
		t.Errorf("defaults wrong: branch=%q remote=%q push=%v", comp.Branch, comp.Remote, comp.Push)
	}
	if got := comp.RenderTag("0.5.0"); got != "v0.5.0" {
		t.Errorf("RenderTag = %q, want v0.5.0", got)
	}
	if got := comp.RenderCommit("0.5.0", "v0.5.0"); got != "release: v0.5.0" {
		t.Errorf("RenderCommit = %q", got)
	}
	if cfg.ProjectName != filepath.Base(dir) {
		t.Errorf("ProjectName = %q, want the directory name", cfg.ProjectName)
	}
	if comp.Label() != filepath.Base(dir) {
		t.Errorf("Label() = %q, want the project name", comp.Label())
	}
}

// A VERSION file beats a package.json: a project with both keeps the plain file
// as the source of truth.
func TestDetectPrefersVersionFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")
	write(t, dir, "package.json", `{"version": "0.4.0"}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Only().Source().Path(); got != "VERSION" {
		t.Errorf("source = %q, want VERSION", got)
	}
}

func TestDetectPackageJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"version": "0.4.0"}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Only().Source().Describe(); got != "package.json#version" {
		t.Errorf("source = %q", got)
	}
}

func TestDetectNothingFound(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("a repository with no version file should be an error")
	}
}

// The shape the README documents, read back key by key.
func TestLoadList(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0")
	cfg := load(t, dir, `
project: hop

version:
  - VERSION
  - package.json#version
  - charts/app/Chart.yaml#appVersion

release:
  branch: release
  remote: upstream
  tag: "{{ version }}"
  commit: "chore: {{version}} ({{tag}})"
  push: false
  prerelease: rc
`)

	if cfg.ProjectName != "hop" {
		t.Errorf("ProjectName = %q", cfg.ProjectName)
	}
	comp := cfg.Only()
	want := []string{"VERSION", "package.json#version", "charts/app/Chart.yaml#appVersion"}
	var got []string
	for _, s := range comp.Sources {
		got = append(got, s.Describe())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sources = %v, want %v", got, want)
	}
	if comp.Branch != "release" || comp.Remote != "upstream" || comp.Push {
		t.Errorf("release block not applied: %+v", comp)
	}
	if comp.PreID != "rc" {
		t.Errorf("PreID = %q", comp.PreID)
	}
	if got := comp.RenderTag("1.0.0"); got != "1.0.0" {
		t.Errorf("RenderTag = %q", got)
	}
	if got := comp.RenderCommit("1.0.0", "1.0.0"); got != "chore: 1.0.0 (1.0.0)" {
		t.Errorf("RenderCommit = %q", got)
	}
}

// A single location may be written without the list, which is what a one-file
// component looks like.
func TestLoadSingleVersionScalar(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "web/package.json", `{"version": "0.4.0"}`)
	cfg := load(t, dir, "version: web/package.json#version\n")
	if got := cfg.Only().Source().Describe(); got != "web/package.json#version" {
		t.Errorf("source = %q", got)
	}
}

// The written-out form is still accepted, for a path that needs an explicit
// type.
func TestLoadWrittenOutEntry(t *testing.T) {
	dir := t.TempDir()
	cfg := load(t, dir, `
version:
  - path: version.conf
    type: yaml
    field: app.version
`)
	if got := cfg.Only().Source().Describe(); got != "version.conf#app.version" {
		t.Errorf("source = %q", got)
	}
}

// A config that configures the release but not the version falls back to
// detection, so `release:` alone is a useful file.
func TestLoadPartialConfigDetectsSource(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")
	cfg := load(t, dir, "release:\n  branch: develop\n")
	if got := cfg.Only().Source().Path(); got != "VERSION" {
		t.Errorf("source = %q, want the detected VERSION", got)
	}
	if cfg.Only().Branch != "develop" {
		t.Error("release.branch was not applied")
	}
}

func TestComponents(t *testing.T) {
	dir := t.TempDir()
	cfg := load(t, dir, `
project: mono

release:
  branch: main
  remote: origin
  prerelease: rc
  tag: "{{component}}-v{{version}}"

components:
  cli:
    version:
      - VERSION
      - package.json#version
    tag: v{{version}}
  web:
    version: web/package.json#version
    branch: web-release
    push: false
`)
	if !cfg.Multi {
		t.Fatal("Multi should be true")
	}
	if got := strings.Join(cfg.Names(), ","); got != "cli,web" {
		t.Errorf("Names() = %q, want the file's order", got)
	}

	cli := cfg.Lookup("cli")
	if cli == nil {
		t.Fatal("cli not found")
	}
	if got := cli.RenderTag("1.2.0"); got != "v1.2.0" {
		t.Errorf("cli tag = %q, the component override did not win", got)
	}
	if len(cli.Sources) != 2 || cli.Mirrors()[0].Describe() != "package.json#version" {
		t.Errorf("cli sources = %v", cli.Paths())
	}
	if cli.Branch != "main" || cli.Remote != "origin" || cli.PreID != "rc" || !cli.Push {
		t.Errorf("cli did not inherit the release block: %+v", cli)
	}
	if cli.Label() != "cli" {
		t.Errorf("Label() = %q", cli.Label())
	}

	web := cfg.Lookup("web")
	if got := web.RenderTag("1.2.0"); got != "web-v1.2.0" {
		t.Errorf("web tag = %q, {{component}} was not expanded", got)
	}
	if web.Branch != "web-release" {
		t.Errorf("web branch = %q, the override did not win", web.Branch)
	}
	if web.Push {
		t.Error("web push override did not win")
	}
	if web.Remote != "origin" || web.PreID != "rc" {
		t.Error("web should still inherit the keys it did not name")
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"unknown top-level key", "versions:\n  - VERSION\n", "unknown key"},
		{"unknown release key", "version: VERSION\nrelease:\n  brunch: main\n", "unknown key"},
		{"unknown component key", "components:\n  a:\n    version: VERSION\n    tagg: x\n", "unknown key"},
		{"typo in the tag placeholder", "version: VERSION\nrelease:\n  tag: \"v{{ vesion }}\"\n", "unknown placeholder"},
		{"tag without a version", "version: VERSION\nrelease:\n  tag: \"release\"\n", "{{ version }}"},
		{"commit without a placeholder", "version: VERSION\nrelease:\n  commit: \"bump\"\n", "no placeholder"},
		{"the same file twice", "version:\n  - VERSION\n  - VERSION\n", "already listed"},
		{"an absolute path", "version: /etc/passwd\n", "absolute"},
		{"a path walking out", "version: ../other/VERSION\n", "walks up"},
		{"a component and a top-level version", "version: VERSION\ncomponents:\n  a:\n    version: A\n", "inside each component"},
		{"a component without a version", "components:\n  a:\n    tag: v{{version}}\n", "has no version"},
		{"an empty components block", "components: {}\n", "name at least one"},
		{"a component named after a bump", "components:\n  minor:\n    version: VERSION\n", "bump keyword"},
		{"a component name with capitals", "components:\n  Web:\n    version: VERSION\n", "lowercase"},
		{"two components claiming one file", "components:\n  a:\n    version: VERSION\n    tag: a-v{{version}}\n  b:\n    version: VERSION\n    tag: b-v{{version}}\n", "cannot hold two versions"},
		{"two components with one tag", "components:\n  a:\n    version: A\n  b:\n    version: B\n", "would both tag"},
		{"{{component}} without components", "version: VERSION\nrelease:\n  tag: \"{{component}}-v{{version}}\"\n", "unknown placeholder"},
	}
	for _, tc := range tests {
		dir := t.TempDir()
		err := loadErr(t, dir, tc.yaml)
		if err == nil {
			t.Errorf("%s: was accepted, want an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// A config error has to say which line to look at.
func TestErrorsCarryTheLineNumber(t *testing.T) {
	dir := t.TempDir()
	err := loadErr(t, dir, "project: x\nversion: VERSION\nrelease:\n  brunch: main\n")
	if err == nil || !strings.Contains(err.Error(), ":4:") {
		t.Errorf("err = %v, want it to point at line 4", err)
	}
}

// The superseded source/mirrors shape still loads, so an existing config keeps
// working, and says so through Legacy.
func TestLegacyShapeStillLoads(t *testing.T) {
	dir := t.TempDir()
	cfg := load(t, dir, `
project:
  name: hop

version:
  source:
    type: file
    path: VERSION
  mirrors:
    - type: json
      path: package.json
      field: version

release:
  branch: main
  tag: "v{{ version }}"
`)
	if !cfg.Legacy {
		t.Error("Legacy should be set for the source/mirrors shape")
	}
	if cfg.ProjectName != "hop" {
		t.Errorf("ProjectName = %q, the nested project.name was not read", cfg.ProjectName)
	}
	comp := cfg.Only()
	if comp.Source().Path() != "VERSION" || len(comp.Mirrors()) != 1 {
		t.Errorf("sources = %v", comp.Paths())
	}
	if got := comp.Mirrors()[0].Describe(); got != "package.json#version" {
		t.Errorf("mirror = %q", got)
	}
}

func TestExpandTolerantOfWhitespace(t *testing.T) {
	comp := &Component{TagTemplate: "v{{ version }}", CommitTemplate: "release: {{tag}} / {{ version }}"}
	if got := comp.RenderTag("1.0.0"); got != "v1.0.0" {
		t.Errorf("RenderTag = %q", got)
	}
	if got := comp.RenderCommit("1.0.0", "v1.0.0"); got != "release: v1.0.0 / 1.0.0" {
		t.Errorf("RenderCommit = %q", got)
	}
}

// A repository that says nothing about a changelog still carries the defaults,
// so the rest of stamp has values to work with even while the feature is off.
func TestChangelogDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "1.0.0\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cl := cfg.Only().Changelog
	if cl.File != DefaultChangelogFile || cl.Dir != DefaultChangelogDir {
		t.Errorf("file = %q, dir = %q", cl.File, cl.Dir)
	}
	if cl.Fallback != DefaultChangelogFallback || cl.Require || !cl.TagBody {
		t.Errorf("fallback = %q require = %v tag_body = %v", cl.Fallback, cl.Require, cl.TagBody)
	}
	if cl.Declared {
		t.Error("Declared should be false without a changelog block")
	}
}

func TestChangelogEveryKey(t *testing.T) {
	dir := t.TempDir()
	cfg := load(t, dir, `
version: VERSION

changelog:
  file: docs/CHANGES.md
  dir: .changes
  fallback: none
  require: true
  tag_body: false
`)
	cl := cfg.Only().Changelog
	if cl.File != "docs/CHANGES.md" || cl.Dir != ".changes" {
		t.Errorf("file = %q, dir = %q", cl.File, cl.Dir)
	}
	if cl.Fallback != "none" || !cl.Require || cl.TagBody {
		t.Errorf("fallback = %q require = %v tag_body = %v", cl.Fallback, cl.Require, cl.TagBody)
	}
	if !cl.Declared {
		t.Error("Declared should be set by a changelog block")
	}
}

// A component's changelog overrides the keys it names and inherits the rest
// from the top-level block, exactly like the release keys.
func TestChangelogComponentOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := load(t, dir, `
changelog:
  dir: .changes
  fallback: none
  require: true

components:
  cli:
    version: VERSION
    tag: cli-v{{version}}
    changelog:
      file: cli/CHANGELOG.md
  web:
    version: web/package.json#version
    tag: web-v{{version}}
    changelog:
      file: web/CHANGELOG.md
      require: false
`)
	cli := cfg.Lookup("cli").Changelog
	if cli.File != "cli/CHANGELOG.md" {
		t.Errorf("cli file = %q, the override did not win", cli.File)
	}
	if cli.Dir != ".changes" || cli.Fallback != "none" || !cli.Require || !cli.TagBody {
		t.Errorf("cli did not inherit the unnamed keys: %+v", cli)
	}

	web := cfg.Lookup("web").Changelog
	if web.File != "web/CHANGELOG.md" || web.Require {
		t.Errorf("web overrides did not win: %+v", web)
	}
	if web.Dir != ".changes" || web.Fallback != "none" {
		t.Errorf("web did not inherit the unnamed keys: %+v", web)
	}
}

// An empty file: is how a repository says "carry the entries into the tag, but
// render no file"; it still declares the block.
func TestChangelogEmptyFileIsLegal(t *testing.T) {
	dir := t.TempDir()
	cfg := load(t, dir, "version: VERSION\nchangelog:\n  file: \"\"\n")
	cl := cfg.Only().Changelog
	if cl.File != "" {
		t.Errorf("file = %q, want it empty", cl.File)
	}
	if !cl.Declared {
		t.Error("an empty file: still declares a changelog block")
	}
	if !cfg.Only().ChangelogEnabled(dir) {
		t.Error("a declared block enables the changelog")
	}
}

func TestChangelogRejects(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"unknown changelog key", "version: VERSION\nchangelog:\n  fil: CHANGELOG.md\n", "unknown key"},
		{"unknown component changelog key", "components:\n  a:\n    version: VERSION\n    changelog:\n      dirr: x\n", "unknown key"},
		{"a fallback that is neither", "version: VERSION\nchangelog:\n  fallback: guess\n", `it must be "commits" or "none"`},
		{"changelog that is not a mapping", "version: VERSION\nchangelog: CHANGELOG.md\n", "must be a mapping"},
		{"an absolute file", "version: VERSION\nchangelog:\n  file: /tmp/CHANGELOG.md\n", "is absolute"},
		{"an absolute dir", "version: VERSION\nchangelog:\n  dir: /tmp/changes\n", "is absolute"},
		{"a file walking out", "version: VERSION\nchangelog:\n  file: ../CHANGELOG.md\n", "walks up"},
		{"a dir walking out", "version: VERSION\nchangelog:\n  dir: ../changes\n", "walks up"},
		{"an empty dir", "version: VERSION\nchangelog:\n  dir: \"\"\n", "name the directory"},
		{
			"two components rendering into one file",
			"changelog:\n  file: CHANGELOG.md\ncomponents:\n  a:\n    version: A\n    tag: a-v{{version}}\n  b:\n    version: B\n    tag: b-v{{version}}\n",
			"would both render into",
		},
	}
	for _, tc := range tests {
		dir := t.TempDir()
		err := loadErr(t, dir, tc.yaml)
		if err == nil {
			t.Errorf("%s: was accepted, want an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// Two components may collect fragments in one directory: each writes into its
// own subdirectory of it.
func TestChangelogComponentsMayShareADir(t *testing.T) {
	dir := t.TempDir()
	cfg := load(t, dir, `
changelog:
  dir: .stamp/changelog

components:
  a:
    version: A
    tag: a-v{{version}}
    changelog:
      file: a/CHANGELOG.md
  b:
    version: B
    tag: b-v{{version}}
    changelog:
      file: b/CHANGELOG.md
`)
	if cfg.Lookup("a").Changelog.Dir != cfg.Lookup("b").Changelog.Dir {
		t.Error("both components should have inherited the shared dir")
	}
}

// The feature is opt-in by use: it turns on when the config declares it, when
// fragments have been written, or when a changelog file is already there.
func TestChangelogEnabled(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "VERSION", "1.0.0\n")
		cfg, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Only().ChangelogEnabled(dir) {
			t.Error("a repository that says nothing about a changelog has it off")
		}
	})

	t.Run("declared in the config", func(t *testing.T) {
		dir := t.TempDir()
		cfg := load(t, dir, "version: VERSION\nchangelog:\n  fallback: none\n")
		write(t, dir, "VERSION", "1.0.0\n")
		if !cfg.Only().ChangelogEnabled(dir) {
			t.Error("a declared block turns the changelog on")
		}
	})

	t.Run("the fragment directory exists", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "VERSION", "1.0.0\n")
		if err := os.MkdirAll(filepath.Join(dir, DefaultChangelogDir), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Only().ChangelogEnabled(dir) {
			t.Error("written fragments turn the changelog on")
		}
	})

	t.Run("the changelog file exists", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "VERSION", "1.0.0\n")
		write(t, dir, DefaultChangelogFile, "# Changelog\n")
		cfg, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Only().ChangelogEnabled(dir) {
			t.Error("an existing changelog file turns the changelog on")
		}
	})
}
