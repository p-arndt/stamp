// Package source reads and writes the places a project keeps its version.
//
// Four kinds are supported: a plain text file (the VERSION-file convention) and
// a field inside a JSON, YAML or TOML document. Writing is deliberately
// surgical: the file is edited in place by replacing just the version literal,
// never by re-marshalling it, because a marshal round-trip would reformat the
// whole file (key order, indentation, comments, tabs vs spaces) and bury the
// version bump in a hundred-line diff.
//
// In .stamp.yml a location is usually written in its shorthand form,
// "path#field":
//
//	VERSION                          a plain text file
//	package.json#version             a JSON field
//	charts/app/Chart.yaml#appVersion a YAML field
//	pyproject.toml#project.version   a nested TOML field
//
// The kind follows from the extension, so it almost never has to be spelled
// out; ParseSpec and Spec.Normalize do that resolution.
package source

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kinds of version location, as they may appear in .stamp.yml.
const (
	KindFile = "file"
	KindJSON = "json"
	KindYAML = "yaml"
	KindTOML = "toml"
)

// DefaultField is the field name assumed for a structured file whose shorthand
// names no field: package.json means package.json#version.
const DefaultField = "version"

// Source is one place a version is stored.
type Source interface {
	// Path is the location's path relative to the repository root.
	Path() string
	// Read returns the version currently stored there.
	Read() (string, error)
	// Write stores version, preserving the rest of the file byte for byte.
	Write(version string) error
	// Describe renders the location the way it is written in the config, e.g.
	// "package.json#version".
	Describe() string
}

// Spec is one version location in its declarative form: what the config says,
// before it is bound to a repository root.
type Spec struct {
	// Path is relative to the repository root.
	Path string
	// Type is one of the Kind constants. Empty means "derive from the path".
	Type string
	// Field is a dot-separated path into a structured document. Empty means
	// the kind's default, and is meaningless for KindFile.
	Field string
}

// ParseSpec reads the shorthand form "path#field". The part after the first
// "#" is the field path; a path with no "#" names no field.
//
// The first "#" wins rather than the last, because a field path may not contain
// one but a file name theoretically could, and splitting early gives the
// clearer error either way.
func ParseSpec(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Spec{}, fmt.Errorf("empty version location")
	}
	path, field, hasField := strings.Cut(s, "#")
	if path == "" {
		return Spec{}, fmt.Errorf("%q names a field but no file", s)
	}
	if hasField && field == "" {
		return Spec{}, fmt.Errorf("%q ends in \"#\"; write the field after it, or drop the \"#\"", s)
	}
	return Spec{Path: path, Field: field}, nil
}

// Shorthand renders a spec back into its "path#field" form, for `stamp init`
// and for error messages.
func (s Spec) Shorthand() string {
	if s.Field == "" {
		return s.Path
	}
	return s.Path + "#" + s.Field
}

// NeedsType reports whether the spec's kind cannot be derived from its path, so
// `stamp init` knows when it has to write an explicit type.
func (s Spec) NeedsType() bool {
	return s.Type != "" && s.Type != KindOf(s.Path)
}

// Normalize resolves Type and Field to their effective values and rejects the
// combinations that cannot mean anything.
func (s Spec) Normalize() (Spec, error) {
	if err := checkRelative(s.Path); err != nil {
		return Spec{}, err
	}
	if s.Type == "" {
		s.Type = KindOf(s.Path)
	}
	switch s.Type {
	case KindFile:
		if s.Field != "" {
			return Spec{}, fmt.Errorf("%s holds nothing but the version, so it has no field %q; drop the \"#%s\", or set an explicit type", s.Path, s.Field, s.Field)
		}
	case KindJSON, KindYAML, KindTOML:
		if s.Field == "" {
			s.Field = DefaultField
		}
		if err := checkField(s.Field); err != nil {
			return Spec{}, err
		}
	default:
		return Spec{}, fmt.Errorf("unknown type %q; use %s, %s, %s or %s",
			s.Type, KindFile, KindJSON, KindYAML, KindTOML)
	}
	return s, nil
}

// KindOf names the kind implied by a path's extension. Anything unrecognised is
// a plain text file, which is what a bare "VERSION" is.
func KindOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return KindJSON
	case ".yaml", ".yml":
		return KindYAML
	case ".toml":
		return KindTOML
	default:
		return KindFile
	}
}

// New binds a spec to a repository root. The spec is normalized first, so a
// caller may pass one straight from the config.
func New(root string, s Spec) (Source, error) {
	spec, err := s.Normalize()
	if err != nil {
		return nil, err
	}
	base := base{root: root, rel: spec.Path, field: spec.Field, shown: spec.Shorthand()}
	switch spec.Type {
	case KindFile:
		return &fileSource{base}, nil
	case KindJSON:
		return &jsonSource{base}, nil
	case KindYAML:
		return &yamlSource{base}, nil
	case KindTOML:
		return &tomlSource{base}, nil
	}
	return nil, fmt.Errorf("unknown type %q", spec.Type)
}

// base is the state every kind shares.
type base struct {
	root  string
	rel   string
	field string
	shown string
}

func (b base) Path() string     { return b.rel }
func (b base) Describe() string { return b.shown }
func (b base) abs() string      { return filepath.Join(b.root, b.rel) }

// keys splits the field path into its segments.
func (b base) keys() []string { return strings.Split(b.field, ".") }

// checkField rejects field paths that cannot be walked.
func checkField(field string) error {
	for _, part := range strings.Split(field, ".") {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("field path %q has an empty segment; write it as \"a.b\"", field)
		}
	}
	return nil
}

// checkRelative rejects any path that could resolve outside the repository.
//
// filepath.IsAbs alone is not enough, because it answers differently per
// platform: on Windows "/etc/passwd" is *not* absolute (it carries no drive) and
// `C:\x` is not absolute on unix. A config file travels between machines, so
// both forms are rejected everywhere, along with "..", which would walk out of
// the repository the long way round.
func checkRelative(path string) error {
	if path == "" {
		return fmt.Errorf("a version location needs a path")
	}
	bad := func(why string) error {
		return fmt.Errorf("version location %q %s; it must be relative to the repository root", path, why)
	}

	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return bad("is absolute")
	}
	// A drive-qualified Windows path: "C:", "C:x", "C:\x".
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

// writeKeepingMode replaces a file's contents without changing its permissions.
// A version file may well be executable or group-writable for reasons that have
// nothing to do with stamp, and a release should not quietly reset that.
func writeKeepingMode(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return os.WriteFile(path, data, mode)
}

// splice replaces b[start:end] with replacement.
func splice(b []byte, start, end int, replacement []byte) []byte {
	out := make([]byte, 0, len(b)-(end-start)+len(replacement))
	out = append(out, b[:start]...)
	out = append(out, replacement...)
	out = append(out, b[end:]...)
	return out
}
