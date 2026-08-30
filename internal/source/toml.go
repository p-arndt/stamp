package source

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// tomlSource is a field inside a TOML document, addressed by its full key path:
// pyproject.toml#project.version, Cargo.toml#package.version.
type tomlSource struct{ base }

// Read decodes the document with a real TOML parser, so the value stamp reports
// is the value the language defines. The scanner below is only ever used to
// find where to write, and is checked against this.
func (s *tomlSource) Read() (string, error) {
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err := toml.Unmarshal(b, &doc); err != nil {
		return "", fmt.Errorf("%s is not valid TOML: %w", s.rel, err)
	}
	v, err := walkDoc(doc, s.keys())
	if err != nil {
		return "", fmt.Errorf("%s: %w", s.rel, err)
	}
	return v, nil
}

func (s *tomlSource) Write(version string) error {
	current, err := s.Read()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return err
	}
	start, end, err := locateTOML(b, s.keys())
	if err != nil {
		return fmt.Errorf("%s: %w", s.rel, err)
	}
	// The safety net: the scanner and the parser have to agree on what is being
	// replaced. They are independent implementations, so a disagreement means
	// the scanner landed somewhere else and writing would corrupt the file.
	if got := unquoteTOML(string(b[start:end])); got != current {
		return fmt.Errorf("%s: cannot locate %s in the file (found %q, expected %q)",
			s.rel, pathOf(s.keys()), got, current)
	}
	// A semver version needs no escaping, and a basic string accepts it as is.
	return writeKeepingMode(s.abs(), splice(b, start, end, []byte(`"`+version+`"`)))
}

// locateTOML returns the byte span of the quoted string at the given key path,
// its quotes included.
//
// TOML is scanned line by line rather than parsed: the parser that produced the
// value cannot say where in the file it came from, and rewriting the document
// from the parsed tree would drop every comment and reorder every table. The
// scanner only ever reports a span it can identify as a single-line quoted
// string at exactly the requested key path; the caller checks that span against
// the parsed value before writing.
func locateTOML(b []byte, keys []string) (start, end int, err error) {
	var table []string
	offset := 0

	for _, line := range splitLinesKeepingOffsets(b) {
		text := string(b[line.start:line.end])
		offset = line.start
		trimmed := strings.TrimLeft(text, " \t")
		lead := len(text) - len(trimmed)

		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
			continue
		case strings.HasPrefix(trimmed, "["):
			header := strings.TrimPrefix(trimmed, "[")
			if strings.HasPrefix(header, "[") {
				// An array of tables. Its entries repeat, so a key path inside
				// one is ambiguous and stamp will not guess which entry.
				header = strings.TrimPrefix(header, "[")
			}
			parts, _, ok := parseTOMLKey(header)
			if !ok {
				continue
			}
			table = parts
			continue
		}

		parts, rest, ok := parseTOMLKey(trimmed)
		if !ok {
			continue
		}
		rest = strings.TrimLeft(rest, " \t")
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		if !samePath(append(append([]string{}, table...), parts...), keys) {
			continue
		}

		value := strings.TrimLeft(rest[1:], " \t")
		valueStart := offset + lead + (len(trimmed) - len(value))
		length, err := tomlStringLength(value)
		if err != nil {
			return 0, 0, fmt.Errorf("%s: %w", pathOf(keys), err)
		}
		return valueStart, valueStart + length, nil
	}
	return 0, 0, fmt.Errorf("no %s key found on a line stamp can rewrite", pathOf(keys))
}

// tomlStringLength returns the length of the quoted string at the start of s.
func tomlStringLength(s string) (int, error) {
	switch {
	case strings.HasPrefix(s, `"""`), strings.HasPrefix(s, `'''`):
		return 0, fmt.Errorf("is a multi-line string; a version literal must be a single-line quoted string")
	case strings.HasPrefix(s, `"`):
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == '"' {
				return i + 1, nil
			}
		}
		return 0, fmt.Errorf("unterminated string")
	case strings.HasPrefix(s, `'`):
		if i := strings.IndexByte(s[1:], '\''); i >= 0 {
			return i + 2, nil
		}
		return 0, fmt.Errorf("unterminated string")
	default:
		return 0, fmt.Errorf("is not a quoted string")
	}
}

// unquoteTOML reads a single-line quoted string token back into its value.
func unquoteTOML(token string) string {
	switch {
	case strings.HasPrefix(token, `'`) && strings.HasSuffix(token, `'`) && len(token) >= 2:
		return token[1 : len(token)-1] // a literal string has no escapes
	case strings.HasPrefix(token, `"`) && strings.HasSuffix(token, `"`) && len(token) >= 2:
		var doc struct {
			V string `toml:"v"`
		}
		if err := toml.Unmarshal([]byte("v = "+token), &doc); err == nil {
			return doc.V
		}
	}
	return token
}

// parseTOMLKey reads a dotted key (bare, quoted, or a mix) from the start of
// s, and returns its segments plus whatever follows.
func parseTOMLKey(s string) (parts []string, rest string, ok bool) {
	rest = s
	for {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return nil, "", false
		}
		var part string
		switch rest[0] {
		case '"', '\'':
			length, err := tomlStringLength(rest)
			if err != nil {
				return nil, "", false
			}
			part, rest = unquoteTOML(rest[:length]), rest[length:]
		default:
			end := strings.IndexFunc(rest, func(r rune) bool {
				return !(r == '-' || r == '_' ||
					(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
			})
			if end == 0 {
				return nil, "", false
			}
			if end < 0 {
				end = len(rest)
			}
			part, rest = rest[:end], rest[end:]
		}
		parts = append(parts, part)

		rest = strings.TrimLeft(rest, " \t")
		if strings.HasPrefix(rest, ".") {
			rest = rest[1:]
			continue
		}
		return parts, rest, true
	}
}

func samePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lineSpan is one line of a file, without its terminator.
type lineSpan struct{ start, end int }

func splitLinesKeepingOffsets(b []byte) []lineSpan {
	var lines []lineSpan
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == '\n' {
			end := i
			if end > start && b[end-1] == '\r' {
				end--
			}
			lines = append(lines, lineSpan{start: start, end: end})
			start = i + 1
		}
	}
	return lines
}
