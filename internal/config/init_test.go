package config

import (
	"strings"
	"testing"
)

// draftOf renders a config for the repository and loads it back, which is the
// property init has to hold above all: what it writes, stamp reads.
func draftOf(t *testing.T, dir string, o InitOptions) *Draft {
	t.Helper()
	d, err := Init(dir, o)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return d
}

func paths(comp *Component) string { return strings.Join(comp.Paths(), ",") }

func TestInitVersionFileRoundTrips(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")

	d := draftOf(t, dir, InitOptions{})
	cfg := d.Config
	if cfg.Multi {
		t.Error("one version file is not a set of components")
	}
	if got := paths(cfg.Only()); got != "VERSION" {
		t.Errorf("sources = %q", got)
	}
	if got := cfg.Only().RenderTag("0.4.0"); got != "v0.4.0" {
		t.Errorf("tag = %q", got)
	}
	// The generated file is meant to be read and edited, so the defaults are
	// written out rather than left implicit.
	for _, want := range []string{"project:", "version:", "  - VERSION", "release:", "branch:", "remote:", "tag:", "commit:", "push:", "prerelease:"} {
		if !strings.Contains(d.YAML, want) {
			t.Errorf("the rendered config is missing %q:\n%s", want, d.YAML)
		}
	}
}

// A repository with a VERSION file and a package.json beside it versions one
// thing in two files: the plain file leads, the manifest follows.
func TestInitAddsPackageJSONAsASecondLocation(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")
	write(t, dir, "package.json", `{"version": "0.4.0"}`)

	cfg := draftOf(t, dir, InitOptions{}).Config
	if cfg.Multi {
		t.Error("two files in one directory are one component")
	}
	if got := paths(cfg.Only()); got != "VERSION,package.json" {
		t.Errorf("sources = %q, want VERSION first", got)
	}
}

func TestInitNodeProject(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"version": "0.4.0"}`)

	cfg := draftOf(t, dir, InitOptions{}).Config
	if got := cfg.Only().Source().Describe(); got != "package.json#version" {
		t.Errorf("source = %q", got)
	}
}

// Version files in separate directories are separate components, each named
// after its directory and told apart by its tag.
func TestInitDetectsComponents(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")
	write(t, dir, "web/package.json", `{"version": "1.0.0"}`)
	write(t, dir, "services/api/pyproject.toml", "[project]\nversion = \"2.0.0\"\n")

	d := draftOf(t, dir, InitOptions{})
	cfg := d.Config
	if !cfg.Multi {
		t.Fatalf("three directories should become components:\n%s", d.YAML)
	}
	// The repository root has no directory to be named after, so it takes the
	// project's name, and it comes first.
	if got := cfg.Names()[0]; got == "" || got == "api" || got == "web" {
		t.Errorf("the root component is named %q, want the project name", got)
	}
	if cfg.Lookup("web") == nil || cfg.Lookup("api") == nil {
		t.Fatalf("expected web and api components, got %v", cfg.Names())
	}
	if got := paths(cfg.Lookup("api")); got != "services/api/pyproject.toml" {
		t.Errorf("api sources = %q", got)
	}
	// Two components must not want the same tag, so the generated template
	// carries the component name.
	if got := cfg.Lookup("web").RenderTag("1.0.0"); got != "web-v1.0.0" {
		t.Errorf("web tag = %q", got)
	}
}

// Single collapses a component layout into one version, which is what
// answering "no" to init's question does.
func TestInitSingleMergesEverything(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")
	write(t, dir, "web/package.json", `{"version": "0.4.0"}`)

	cfg := draftOf(t, dir, InitOptions{Single: true}).Config
	if cfg.Multi {
		t.Fatal("Single should produce one component")
	}
	if got := paths(cfg.Only()); got != "VERSION,web/package.json" {
		t.Errorf("sources = %q", got)
	}
}

// A directory init has no business in must not become a component.
func TestInitSkipsVendoredDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")
	write(t, dir, "node_modules/left-pad/package.json", `{"version": "1.0.0"}`)
	write(t, dir, "vendor/x/Cargo.toml", "[package]\nversion = \"1.0.0\"\n")

	cfg := draftOf(t, dir, InitOptions{}).Config
	if cfg.Multi {
		t.Errorf("vendored manifests became components: %v", cfg.Names())
	}
}

func TestInitOverrides(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")

	cfg := draftOf(t, dir, InitOptions{
		Name:   "hop",
		Branch: "develop",
		Remote: "upstream",
		Tag:    "{{version}}",
		Commit: "bump to {{version}}",
		PreID:  "rc",
	}).Config

	comp := cfg.Only()
	if cfg.ProjectName != "hop" || comp.Branch != "develop" || comp.Remote != "upstream" || comp.PreID != "rc" {
		t.Errorf("overrides not applied: %+v", comp)
	}
	if got := comp.RenderTag("0.5.0"); got != "0.5.0" {
		t.Errorf("tag = %q", got)
	}
	if got := comp.RenderCommit("0.5.0", "0.5.0"); got != "bump to 0.5.0" {
		t.Errorf("commit = %q", got)
	}
}

// Explicit locations replace detection entirely, in the order they were given.
func TestInitExplicitVersions(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")

	cfg := draftOf(t, dir, InitOptions{
		Versions: []string{"app/Chart.yaml#appVersion", "package.json"},
	}).Config
	if got := paths(cfg.Only()); got != "app/Chart.yaml,package.json" {
		t.Errorf("sources = %q", got)
	}
	if got := cfg.Only().Mirrors()[0].Describe(); got != "package.json#version" {
		t.Errorf("the JSON default field was not filled in: %q", got)
	}
}

func TestInitRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		opts InitOptions
	}{
		{"an absolute path", InitOptions{Versions: []string{"/etc/passwd"}}},
		{"a path walking out", InitOptions{Versions: []string{"../x/VERSION"}}},
		{"a field on a plain file", InitOptions{Versions: []string{"VERSION#version"}}},
		{"a tag with a typo", InitOptions{Versions: []string{"VERSION"}, Tag: "v{{ vesion }}"}},
		{"a tag with no version", InitOptions{Versions: []string{"VERSION"}, Tag: "release"}},
	}
	for _, tc := range tests {
		if _, err := Init(t.TempDir(), tc.opts); err == nil {
			t.Errorf("%s: was accepted, want an error", tc.name)
		}
	}
}

func TestInitNeedsSomethingToFind(t *testing.T) {
	if _, err := Init(t.TempDir(), InitOptions{}); err == nil {
		t.Error("an empty repository should be an error")
	}
}
