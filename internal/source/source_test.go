package source

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

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFileSource(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\n")

	s, err := New(dir, KindFile, "VERSION", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(); err != nil || got != "0.4.0" {
		t.Fatalf("Read() = %q, %v", got, err)
	}
	if err := s.Write("0.5.0"); err != nil {
		t.Fatal(err)
	}
	// The trailing newline the file had must survive, so the diff stays one line.
	if got := read(t, dir, "VERSION"); got != "0.5.0\n" {
		t.Errorf("contents = %q, want %q", got, "0.5.0\n")
	}
}

func TestFileSourceWithoutTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0")

	s, _ := New(dir, KindFile, "VERSION", "")
	if err := s.Write("0.5.0"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "VERSION"); got != "0.5.0" {
		t.Errorf("contents = %q, want %q — a newline was added", got, "0.5.0")
	}
}

func TestFileSourceRejectsMultiLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\nsomething else\n")
	s, _ := New(dir, KindFile, "VERSION", "")
	if _, err := s.Read(); err == nil {
		t.Error("a multi-line version file should be rejected")
	}
}

// The important property of the JSON source: everything except the version
// literal comes out byte for byte identical, including tab indentation, key
// order and the trailing newline.
func TestJSONSourcePreservesFormatting(t *testing.T) {
	const before = "{\n\t\"name\": \"uprox\",\n\t\"version\": \"0.22.2\",\n\t\"private\": true,\n\t\"scripts\": {\n\t\t\"build\": \"vite build\"\n\t}\n}\n"
	const after = "{\n\t\"name\": \"uprox\",\n\t\"version\": \"0.23.0\",\n\t\"private\": true,\n\t\"scripts\": {\n\t\t\"build\": \"vite build\"\n\t}\n}\n"

	dir := t.TempDir()
	write(t, dir, "package.json", before)

	s, err := New(dir, KindJSON, "package.json", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(); err != nil || got != "0.22.2" {
		t.Fatalf("Read() = %q, %v", got, err)
	}
	if err := s.Write("0.23.0"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "package.json"); got != after {
		t.Errorf("contents =\n%q\nwant\n%q", got, after)
	}
}

// A nested "version" key must not be mistaken for the top-level one — the case
// a regexp-based replacement would get wrong.
func TestJSONSourceIgnoresNestedKeys(t *testing.T) {
	const before = `{
  "dependencies": {
    "left-pad": { "version": "1.0.0" }
  },
  "overrides": [ { "version": "9.9.9" } ],
  "version": "0.1.0"
}
`
	dir := t.TempDir()
	write(t, dir, "package.json", before)

	s, _ := New(dir, KindJSON, "package.json", "version")
	if got, err := s.Read(); err != nil || got != "0.1.0" {
		t.Fatalf("Read() = %q, %v", got, err)
	}
	if err := s.Write("0.2.0"); err != nil {
		t.Fatal(err)
	}
	got := read(t, dir, "package.json")
	if want := `"version": "0.2.0"` + "\n}"; !strings.Contains(got, want) {
		t.Errorf("top-level version was not updated:\n%s", got)
	}
	for _, nested := range []string{`"version": "1.0.0"`, `"version": "9.9.9"`} {
		if !strings.Contains(got, nested) {
			t.Errorf("nested %s was modified:\n%s", nested, got)
		}
	}
}

func TestJSONSourceCustomField(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "app.json", `{"appVersion": "1.0.0", "version": "not-this-one"}`)

	s, err := New(dir, KindJSON, "app.json", "appVersion")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read(); got != "1.0.0" {
		t.Fatalf("Read() = %q", got)
	}
	if err := s.Write("1.1.0"); err != nil {
		t.Fatal(err)
	}
	got := read(t, dir, "app.json")
	if !strings.Contains(got, `"appVersion": "1.1.0"`) || !strings.Contains(got, `"version": "not-this-one"`) {
		t.Errorf("wrong field updated: %s", got)
	}
}

func TestJSONSourceErrors(t *testing.T) {
	dir := t.TempDir()

	write(t, dir, "a.json", `{"name": "x"}`)
	s, _ := New(dir, KindJSON, "a.json", "version")
	if _, err := s.Read(); err == nil {
		t.Error("a missing field should be an error")
	}

	write(t, dir, "b.json", `{"version": 3}`)
	s, _ = New(dir, KindJSON, "b.json", "version")
	if _, err := s.Read(); err == nil {
		t.Error("a non-string version should be an error")
	}
	if err := s.Write("1.0.0"); err == nil {
		t.Error("writing over a non-string version should be an error")
	}

	write(t, dir, "c.json", `{"version": {"major": 1}}`)
	s, _ = New(dir, KindJSON, "c.json", "version")
	if err := s.Write("1.0.0"); err == nil {
		t.Error("writing over an object version should be an error")
	}

	write(t, dir, "d.json", `not json at all`)
	s, _ = New(dir, KindJSON, "d.json", "version")
	if _, err := s.Read(); err == nil {
		t.Error("invalid JSON should be an error")
	}
}

func TestNewValidation(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir, "toml", "Cargo.toml", ""); err == nil {
		t.Error("an unknown type should be rejected")
	}
	if _, err := New(dir, KindFile, "", ""); err == nil {
		t.Error("an empty path should be rejected")
	}
	// Rejected on every platform, not just the one whose form it is: a config
	// file travels between machines, and filepath.IsAbs answers differently per
	// OS ("/etc/passwd" is not absolute on Windows, `C:\x` is not on unix).
	for _, escape := range []string{
		"/etc/passwd",
		`\Windows\system32\x`,
		`C:\Windows\x`,
		"C:relative",
		"../outside/VERSION",
		"nested/../../outside/VERSION",
		`nested\..\..\outside\VERSION`,
	} {
		if _, err := New(dir, KindFile, escape, ""); err == nil {
			t.Errorf("path %q should be rejected — it can resolve outside the repository", escape)
		}
	}
	if _, err := New(dir, KindFile, "VERSION", "version"); err == nil {
		t.Error("a field on a file source should be rejected")
	}
	if _, err := New(dir, KindJSON, "package.json", "a.b"); err == nil {
		t.Error("a dotted field path should be rejected")
	}
}
