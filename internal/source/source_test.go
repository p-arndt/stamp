package source

import (
	"os"
	"path/filepath"
	"runtime"
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

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// open builds a source from the shorthand a config would carry.
func open(t *testing.T, dir, shorthand string) Source {
	t.Helper()
	spec, err := ParseSpec(shorthand)
	if err != nil {
		t.Fatalf("ParseSpec(%q): %v", shorthand, err)
	}
	s, err := New(dir, spec)
	if err != nil {
		t.Fatalf("New(%q): %v", shorthand, err)
	}
	return s
}

// roundTrip is the property that matters for every kind: the version comes back
// out, the new one goes in, and nothing else in the file moves.
func roundTrip(t *testing.T, name, shorthand, before, want, after string) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, name, before)

	s := open(t, dir, shorthand)
	got, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != want {
		t.Fatalf("Read = %q, want %q", got, want)
	}
	if err := s.Write("0.23.0"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := read(t, dir, name); got != after {
		t.Errorf("after Write the file is\n%q\nwant\n%q", got, after)
	}
}

func TestParseSpec(t *testing.T) {
	tests := []struct {
		in    string
		path  string
		typ   string
		field string
	}{
		{"VERSION", "VERSION", KindFile, ""},
		{"package.json", "package.json", KindJSON, "version"},
		{"package.json#version", "package.json", KindJSON, "version"},
		{"web/package.json#version", "web/package.json", KindJSON, "version"},
		{"Chart.yaml#appVersion", "Chart.yaml", KindYAML, "appVersion"},
		{"chart.yml#a.b.c", "chart.yml", KindYAML, "a.b.c"},
		{"pyproject.toml#project.version", "pyproject.toml", KindTOML, "project.version"},
		{"Cargo.toml", "Cargo.toml", KindTOML, "version"},
	}
	for _, tc := range tests {
		spec, err := ParseSpec(tc.in)
		if err != nil {
			t.Errorf("ParseSpec(%q): %v", tc.in, err)
			continue
		}
		spec, err = spec.Normalize()
		if err != nil {
			t.Errorf("Normalize(%q): %v", tc.in, err)
			continue
		}
		if spec.Path != tc.path || spec.Type != tc.typ || spec.Field != tc.field {
			t.Errorf("ParseSpec(%q) = %+v, want path=%q type=%q field=%q", tc.in, spec, tc.path, tc.typ, tc.field)
		}
	}
}

func TestParseSpecRejects(t *testing.T) {
	for _, in := range []string{"", "#version", "package.json#", "/etc/passwd", "../outside/VERSION", `C:\x\VERSION`, "VERSION#version", "package.json#a..b"} {
		spec, err := ParseSpec(in)
		if err == nil {
			_, err = spec.Normalize()
		}
		if err == nil {
			t.Errorf("%q was accepted, want an error", in)
		}
	}
}

// Shorthand is what `stamp init` writes back into the config, so it has to
// survive a round trip through ParseSpec.
func TestShorthandRoundTrips(t *testing.T) {
	for _, in := range []string{"VERSION", "package.json#version", "pyproject.toml#project.version"} {
		spec, err := ParseSpec(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := spec.Shorthand(); got != in {
			t.Errorf("Shorthand() = %q, want %q", got, in)
		}
	}
}

func TestFileSource(t *testing.T) {
	roundTrip(t, "VERSION", "VERSION", "0.22.2\n", "0.22.2", "0.23.0\n")
}

// The trailing newline convention of the file has to survive, so the diff stays
// one line either way.
func TestFileSourceWithoutTrailingNewline(t *testing.T) {
	roundTrip(t, "VERSION", "VERSION", "0.22.2", "0.22.2", "0.23.0")
}

func TestFileSourceRejectsMultiLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.4.0\nsomething else\n")
	if _, err := open(t, dir, "VERSION").Read(); err == nil {
		t.Error("a multi-line version file should be rejected")
	}
}

// The important property of every structured kind: everything except the
// version literal comes out byte for byte identical: indentation, key order,
// comments and the trailing newline.
func TestJSONPreservesFormatting(t *testing.T) {
	const before = "{\n\t\"name\": \"uprox\",\n\t\"version\": \"0.22.2\",\n\t\"private\": true,\n\t\"scripts\": {\n\t\t\"build\": \"vite build\"\n\t}\n}\n"
	const after = "{\n\t\"name\": \"uprox\",\n\t\"version\": \"0.23.0\",\n\t\"private\": true,\n\t\"scripts\": {\n\t\t\"build\": \"vite build\"\n\t}\n}\n"
	roundTrip(t, "package.json", "package.json#version", before, "0.22.2", after)
}

// A nested "version" key must not be mistaken for the top-level one, and the
// requested path must be followed exactly.
func TestJSONNestedFields(t *testing.T) {
	const before = `{
  "dependencies": {"left-pad": {"version": "1.0.0"}},
  "app": {"meta": {"version": "0.22.2"}},
  "version": "9.9.9"
}
`
	const after = `{
  "dependencies": {"left-pad": {"version": "1.0.0"}},
  "app": {"meta": {"version": "0.23.0"}},
  "version": "9.9.9"
}
`
	roundTrip(t, "manifest.json", "manifest.json#app.meta.version", before, "0.22.2", after)
}

// The version literal is found structurally, so a "version" inside a nested
// object earlier in the file cannot hijack the write.
func TestJSONIgnoresNestedVersionKeys(t *testing.T) {
	const before = "{\n  \"dependencies\": {\n    \"react\": {\"version\": \"18.0.0\"}\n  },\n  \"version\": \"0.22.2\"\n}\n"
	const after = "{\n  \"dependencies\": {\n    \"react\": {\"version\": \"18.0.0\"}\n  },\n  \"version\": \"0.23.0\"\n}\n"
	roundTrip(t, "package.json", "package.json#version", before, "0.22.2", after)
}

func TestJSONRejects(t *testing.T) {
	tests := []struct{ name, content, shorthand string }{
		{"not json", "not json at all", "package.json#version"},
		{"missing field", `{"name": "x"}`, "package.json#version"},
		{"not a string", `{"version": 3}`, "package.json#version"},
		{"empty", `{"version": "  "}`, "package.json#version"},
		{"path into a scalar", `{"version": "1.0.0"}`, "package.json#version.inner"},
	}
	for _, tc := range tests {
		dir := t.TempDir()
		write(t, dir, "package.json", tc.content)
		if _, err := open(t, dir, tc.shorthand).Read(); err == nil {
			t.Errorf("%s: was accepted, want an error", tc.name)
		}
	}
}

func TestYAMLPreservesCommentsAndQuoting(t *testing.T) {
	const before = `apiVersion: v2
name: app          # the chart's name
version: 0.22.2    # chart version
appVersion: "0.22.2"
`
	const after = `apiVersion: v2
name: app          # the chart's name
version: 0.23.0    # chart version
appVersion: "0.22.2"
`
	roundTrip(t, "Chart.yaml", "Chart.yaml#version", before, "0.22.2", after)
}

// A quoted value stays quoted, in whichever style it was written.
func TestYAMLKeepsQuotingStyle(t *testing.T) {
	roundTrip(t, "Chart.yaml", "Chart.yaml#appVersion",
		"appVersion: \"0.22.2\"\nname: app\n", "0.22.2",
		"appVersion: \"0.23.0\"\nname: app\n")
	roundTrip(t, "Chart.yaml", "Chart.yaml#appVersion",
		"appVersion: '0.22.2'\n", "0.22.2",
		"appVersion: '0.23.0'\n")
}

func TestYAMLNestedField(t *testing.T) {
	const before = "spec:\n  image:\n    tag: 0.22.2\n  replicas: 3\n"
	const after = "spec:\n  image:\n    tag: 0.23.0\n  replicas: 3\n"
	roundTrip(t, "values.yaml", "values.yaml#spec.image.tag", before, "0.22.2", after)
}

func TestYAMLRejects(t *testing.T) {
	tests := []struct{ name, content, shorthand string }{
		{"missing field", "name: app\n", "Chart.yaml#version"},
		{"not a mapping", "- a\n- b\n", "Chart.yaml#version"},
		{"path into a scalar", "version: 1.0.0\n", "Chart.yaml#version.inner"},
	}
	for _, tc := range tests {
		dir := t.TempDir()
		write(t, dir, "Chart.yaml", tc.content)
		if _, err := open(t, dir, tc.shorthand).Read(); err == nil {
			t.Errorf("%s: was accepted, want an error", tc.name)
		}
	}
}

// A block scalar is refused rather than guessed at: a version does not belong
// in one, and rewriting it blind could corrupt the file.
func TestYAMLRefusesBlockScalar(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Chart.yaml", "version: |\n  0.22.2\n")
	if err := open(t, dir, "Chart.yaml#version").Write("0.23.0"); err == nil {
		t.Error("a block scalar should be refused")
	}
}

func TestTOMLPreservesFormatting(t *testing.T) {
	const before = `[project]
name = "app"      # the distribution name
version = "0.22.2"
requires-python = ">=3.11"

[tool.ruff]
version = "9.9.9"
`
	const after = `[project]
name = "app"      # the distribution name
version = "0.23.0"
requires-python = ">=3.11"

[tool.ruff]
version = "9.9.9"
`
	roundTrip(t, "pyproject.toml", "pyproject.toml#project.version", before, "0.22.2", after)
}

// A dependency table holding its own "version" must not be rewritten, and a
// dotted key spelled out on one line has to be found.
func TestTOMLDottedAndNestedKeys(t *testing.T) {
	const before = "[package]\nname = \"app\"\nversion = \"0.22.2\"\n\n[dependencies.serde]\nversion = \"1.0\"\n"
	const after = "[package]\nname = \"app\"\nversion = \"0.23.0\"\n\n[dependencies.serde]\nversion = \"1.0\"\n"
	roundTrip(t, "Cargo.toml", "Cargo.toml#package.version", before, "0.22.2", after)

	roundTrip(t, "x.toml", "x.toml#a.b",
		"a.b = '0.22.2'\nc = 1\n", "0.22.2",
		"a.b = \"0.23.0\"\nc = 1\n")
}

func TestTOMLRejects(t *testing.T) {
	tests := []struct{ name, content, shorthand string }{
		{"invalid toml", "= = =\n", "x.toml#version"},
		{"missing field", "name = \"x\"\n", "x.toml#version"},
		{"not a string", "version = 3\n", "x.toml#version"},
	}
	for _, tc := range tests {
		dir := t.TempDir()
		write(t, dir, "x.toml", tc.content)
		if _, err := open(t, dir, tc.shorthand).Read(); err == nil {
			t.Errorf("%s: was accepted, want an error", tc.name)
		}
	}
}

// A version that only exists in a multi-line string is refused, not mangled.
func TestTOMLRefusesMultiLineString(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.toml", "version = \"\"\"\n0.22.2\n\"\"\"\n")
	if err := open(t, dir, "x.toml#version").Write("0.23.0"); err == nil {
		t.Error("a multi-line string should be refused")
	}
}

// Writing must not change a file's permissions: a version file may be
// executable or group-writable for reasons that are none of stamp's business.
//
// Unix only. Windows has no permission bits to preserve: chmod there toggles
// the read-only flag and nothing else, so a file chmod-ed to 0600 still reads
// back as 0666, and the invariant this test is about does not exist.
func TestWriteKeepsFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix permission bits on Windows")
	}
	dir := t.TempDir()
	write(t, dir, "VERSION", "0.22.2\n")
	path := filepath.Join(dir, "VERSION")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	if err := open(t, dir, "VERSION").Write("0.23.0"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %v, want 0600", got)
	}
}

func TestDescribeIsTheShorthand(t *testing.T) {
	dir := t.TempDir()
	if got := open(t, dir, "VERSION").Describe(); got != "VERSION" {
		t.Errorf("Describe() = %q", got)
	}
	if got := open(t, dir, "package.json#version").Describe(); got != "package.json#version" {
		t.Errorf("Describe() = %q", got)
	}
}

func TestMissingFileReportsThePath(t *testing.T) {
	dir := t.TempDir()
	_, err := open(t, dir, "VERSION").Read()
	if err == nil || !strings.Contains(err.Error(), "VERSION") {
		t.Errorf("err = %v, want it to name the file", err)
	}
}
