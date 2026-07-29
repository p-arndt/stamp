// Package source reads and writes the places a project keeps its version.
//
// Two kinds are supported: a plain text file (the VERSION-file convention) and
// a JSON field (package.json). Writing is deliberately surgical — a JSON file
// is edited in place by replacing just the version literal, never by
// re-marshalling it, because a marshal round-trip would reformat the whole file
// (key order, indentation, tabs vs spaces) and bury the version bump in a
// hundred-line diff.
package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kinds of version source, as they appear in .stamp.yml.
const (
	KindFile = "file"
	KindJSON = "json"
)

// Source is one place a version is stored.
type Source interface {
	// Path is the source's path relative to the repository root.
	Path() string
	// Read returns the version currently stored there.
	Read() (string, error)
	// Write stores version, preserving the rest of the file byte for byte.
	Write(version string) error
	// Describe renders the source for the plan output, e.g.
	// "package.json (version)".
	Describe() string
}

// New builds a Source. root is the repository root; path is relative to it.
// field is only meaningful for the JSON kind and defaults to "version".
func New(root, kind, path, field string) (Source, error) {
	if path == "" {
		return nil, fmt.Errorf("version source of type %q needs a path", kind)
	}
	if filepath.IsAbs(path) {
		return nil, fmt.Errorf("version source path %q must be relative to the repository root", path)
	}

	switch kind {
	case KindFile:
		if field != "" {
			return nil, fmt.Errorf("version source %q is of type file and does not take a field", path)
		}
		return &fileSource{root: root, rel: path}, nil
	case KindJSON:
		if field == "" {
			field = "version"
		}
		if strings.Contains(field, ".") {
			return nil, fmt.Errorf("field %q: only top-level JSON fields are supported", field)
		}
		return &jsonSource{root: root, rel: path, field: field}, nil
	default:
		return nil, fmt.Errorf("unknown version source type %q (use %q or %q)", kind, KindFile, KindJSON)
	}
}

// ---------------------------------------------------------------------------
// file
// ---------------------------------------------------------------------------

type fileSource struct {
	root, rel string
}

func (s *fileSource) Path() string     { return s.rel }
func (s *fileSource) Describe() string { return s.rel }
func (s *fileSource) abs() string      { return filepath.Join(s.root, s.rel) }

func (s *fileSource) Read() (string, error) {
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", s.rel)
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("%s holds more than one line — a version file must contain only the version", s.rel)
	}
	return v, nil
}

// Write replaces the file's contents, keeping whatever trailing newline
// convention it already had so the diff is one line either way.
func (s *fileSource) Write(version string) error {
	trailing := ""
	if old, err := os.ReadFile(s.abs()); err == nil && strings.HasSuffix(string(old), "\n") {
		trailing = "\n"
	}
	return os.WriteFile(s.abs(), []byte(version+trailing), 0o644)
}

// ---------------------------------------------------------------------------
// json
// ---------------------------------------------------------------------------

type jsonSource struct {
	root, rel, field string
}

func (s *jsonSource) Path() string     { return s.rel }
func (s *jsonSource) Describe() string { return fmt.Sprintf("%s (%s)", s.rel, s.field) }
func (s *jsonSource) abs() string      { return filepath.Join(s.root, s.rel) }

// Read decodes the top level with encoding/json, which both validates the file
// and unquotes the value properly.
func (s *jsonSource) Read() (string, error) {
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return "", err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return "", fmt.Errorf("%s is not a JSON object: %w", s.rel, err)
	}
	raw, ok := top[s.field]
	if !ok {
		return "", fmt.Errorf("%s has no top-level %q field", s.rel, s.field)
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("%s: %q is not a string", s.rel, s.field)
	}
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s: %q is empty", s.rel, s.field)
	}
	return strings.TrimSpace(v), nil
}

func (s *jsonSource) Write(version string) error {
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return err
	}
	start, end, err := locateStringValue(b, s.field)
	if err != nil {
		return fmt.Errorf("%s: %w", s.rel, err)
	}
	// The version is escaped as JSON so a value needing escapes can never
	// produce a broken file. In practice a semver string escapes to itself.
	quoted, err := json.Marshal(version)
	if err != nil {
		return err
	}
	out := make([]byte, 0, len(b)+len(quoted))
	out = append(out, b[:start]...)
	out = append(out, quoted...)
	out = append(out, b[end:]...)
	return os.WriteFile(s.abs(), out, 0o644)
}

// locateStringValue finds the byte span of the quoted string value belonging to
// a top-level key, including its surrounding quotes.
//
// This is a minimal JSON scanner rather than a regexp: a regexp for
// `"version"\s*:\s*"…"` would happily match a nested dependency entry that
// happens to have a "version" key, and would rewrite the wrong line.
func locateStringValue(b []byte, field string) (start, end int, err error) {
	const (
		expectKey = iota
		expectValue
	)
	depth, state := 0, expectKey
	// pending: the key just read at depth 1 is the one we want, so the next
	// value token is its value. seen: the key exists but its value is not a
	// string — worth a different error message.
	pending, seen := false, false

	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '{', '[':
			depth++
			if depth == 2 && pending {
				// The wanted key's value is an object or array.
				pending, seen = false, true
			}
			if depth == 1 {
				state = expectKey
			}
		case '}', ']':
			depth--
		case ',':
			if depth == 1 {
				if pending {
					// A scalar that was not a string (number, bool, null).
					pending, seen = false, true
				}
				state = expectKey
			}
		case ':':
			if depth == 1 {
				state = expectValue
			}
		case '"':
			tokStart := i
			i, err = skipString(b, i)
			if err != nil {
				return 0, 0, err
			}
			if depth != 1 {
				continue
			}
			if state == expectKey {
				var key string
				// Reset on every depth-1 key, so a non-matching key can never
				// leave a stale pending behind and hijack a later value.
				pending = json.Unmarshal(b[tokStart:i+1], &key) == nil && key == field
				continue
			}
			if pending {
				return tokStart, i + 1, nil
			}
		}
	}
	if pending || seen {
		return 0, 0, fmt.Errorf("%q is not a string value", field)
	}
	return 0, 0, fmt.Errorf("no top-level %q field found", field)
}

// skipString returns the index of the closing quote of the string starting at
// the opening quote i.
func skipString(b []byte, i int) (int, error) {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++ // skip the escaped byte
		case '"':
			return j, nil
		}
	}
	return 0, fmt.Errorf("unterminated string in JSON")
}
