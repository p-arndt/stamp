package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if cfg.Source.Path() != "VERSION" {
		t.Errorf("source = %q, want VERSION", cfg.Source.Path())
	}
	if cfg.Branch != "main" || cfg.Remote != "origin" || !cfg.Push {
		t.Errorf("defaults wrong: branch=%q remote=%q push=%v", cfg.Branch, cfg.Remote, cfg.Push)
	}
	if got := cfg.RenderTag("0.5.0"); got != "v0.5.0" {
		t.Errorf("RenderTag = %q, want v0.5.0", got)
	}
	if got := cfg.RenderCommit("0.5.0", "v0.5.0"); got != "release: v0.5.0" {
		t.Errorf("RenderCommit = %q", got)
	}
	if cfg.ProjectName != filepath.Base(dir) {
		t.Errorf("ProjectName = %q, want the directory name", cfg.ProjectName)
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
	if cfg.Source.Path() != "VERSION" {
		t.Errorf("source = %q, want VERSION", cfg.Source.Path())
	}
	// It is not silently mirrored, though — that has to be asked for.
	if len(cfg.Mirrors) != 0 {
		t.Errorf("detected %d mirrors, want none", len(cfg.Mirrors))
	}
}

func TestDetectPackageJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"version": "1.0.0"}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source.Path() != "package.json" {
		t.Errorf("source = %q, want package.json", cfg.Source.Path())
	}
}

func TestDetectNothingFound(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("a repository with no version file should be an error")
	} else if !strings.Contains(err.Error(), FileName) {
		t.Errorf("the error should point at %s, got: %v", FileName, err)
	}
}

func TestLoadFull(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")
	write(t, dir, "package.json", `{"version": "0.4.0"}`)
	write(t, dir, FileName, `
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
  branch: release
  remote: upstream
  tag: "{{ version }}"
  commit: "chore: release {{ version }}"
  push: false
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.FromFile {
		t.Error("FromFile should be true")
	}
	if cfg.ProjectName != "hop" || cfg.Branch != "release" || cfg.Remote != "upstream" {
		t.Errorf("got name=%q branch=%q remote=%q", cfg.ProjectName, cfg.Branch, cfg.Remote)
	}
	if cfg.Push {
		t.Error("push: false was ignored")
	}
	// uprox tags without a v prefix — the template has to allow that.
	if got := cfg.RenderTag("0.5.0"); got != "0.5.0" {
		t.Errorf("RenderTag = %q, want 0.5.0", got)
	}
	if got := cfg.RenderCommit("0.5.0", "0.5.0"); got != "chore: release 0.5.0" {
		t.Errorf("RenderCommit = %q", got)
	}
	if len(cfg.Mirrors) != 1 || cfg.Mirrors[0].Path() != "package.json" {
		t.Fatalf("mirrors = %v", cfg.Paths())
	}
	if want := []string{"VERSION", "package.json"}; strings.Join(cfg.Paths(), ",") != strings.Join(want, ",") {
		t.Errorf("Paths() = %v, want %v", cfg.Paths(), want)
	}
}

// A config that only configures the release side still gets its version
// location detected.
func TestLoadPartialConfigDetectsSource(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")
	write(t, dir, FileName, "release:\n  branch: trunk\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Branch != "trunk" {
		t.Errorf("branch = %q", cfg.Branch)
	}
	if cfg.Source.Path() != "VERSION" {
		t.Errorf("source = %q", cfg.Source.Path())
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":            "releese:\n  branch: main\n",
		"typo in placeholder":    "release:\n  tag: \"v{{ vesion }}\"\n",
		"placeholder-free tag":   "release:\n  tag: \"v1.0.0\"\n",
		"unterminated template":  "release:\n  tag: \"v{{ version\"\n",
		"unknown source type":    "version:\n  source:\n    type: toml\n    path: Cargo.toml\n",
		"source without type":    "version:\n  source:\n    path: VERSION\n",
		"mirror duplicates path": "version:\n  source:\n    type: file\n    path: VERSION\n  mirrors:\n    - type: file\n      path: VERSION\n",
	}
	for name, yaml := range cases {
		dir := t.TempDir()
		write(t, dir, "VERSION", "0.4.0\n")
		write(t, dir, FileName, yaml)
		if _, err := Load(dir); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestExpandTolerantOfWhitespace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")
	write(t, dir, FileName, "release:\n  tag: \"v{{version}}\"\n  commit: \"release: {{ tag }}\"\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.RenderTag("1.0.0"); got != "v1.0.0" {
		t.Errorf("RenderTag = %q", got)
	}
	if got := cfg.RenderCommit("1.0.0", "v1.0.0"); got != "release: v1.0.0" {
		t.Errorf("RenderCommit = %q", got)
	}
}
