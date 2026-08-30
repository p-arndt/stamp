package source

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// jsonSource is a field inside a JSON document, addressed by a dot-separated
// path: package.json#version, or manifest.json#app.version.
type jsonSource struct{ base }

// Read decodes the document with encoding/json, which both validates the file
// and unquotes the value properly.
func (s *jsonSource) Read() (string, error) {
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return "", err
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", fmt.Errorf("%s is not valid JSON: %w", s.rel, err)
	}
	v, err := walkDoc(doc, s.keys())
	if err != nil {
		return "", fmt.Errorf("%s: %w", s.rel, err)
	}
	return v, nil
}

func (s *jsonSource) Write(version string) error {
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return err
	}
	start, end, err := locateJSON(b, s.keys())
	if err != nil {
		return fmt.Errorf("%s: %w", s.rel, err)
	}
	// The version is escaped as JSON so a value needing escapes can never
	// produce a broken file. In practice a semver string escapes to itself.
	quoted, err := json.Marshal(version)
	if err != nil {
		return err
	}
	return writeKeepingMode(s.abs(), splice(b, start, end, quoted))
}

// locateJSON returns the byte span of the string value at keys, its surrounding
// quotes included.
//
// This is a minimal JSON scanner rather than a regexp: a regexp for
// `"version"\s*:\s*"…"` would happily match a nested dependency entry that
// happens to have a "version" key, and would rewrite the wrong line. The
// scanner walks the document structurally, so it can only ever land on the key
// path that was asked for.
func locateJSON(b []byte, keys []string) (start, end int, err error) {
	c := &jsonCursor{b: b}
	start, end, err = c.find(keys, nil)
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

type jsonCursor struct {
	b []byte
	i int
}

// find walks keys down from the value at the cursor and returns the span of the
// string it ends at. seen is the path walked so far, for error messages.
func (c *jsonCursor) find(keys, seen []string) (start, end int, err error) {
	c.space()
	if c.i >= len(c.b) {
		return 0, 0, fmt.Errorf("unexpected end of document")
	}

	if len(keys) == 0 {
		if c.b[c.i] != '"' {
			return 0, 0, fmt.Errorf("%s is not a string value", pathOf(seen))
		}
		return c.str()
	}

	if c.b[c.i] != '{' {
		return 0, 0, fmt.Errorf("%s is not an object, so it has no %q inside it", pathOf(seen), keys[0])
	}
	c.i++ // past '{'

	for {
		c.space()
		if c.i >= len(c.b) {
			return 0, 0, fmt.Errorf("unterminated object")
		}
		if c.b[c.i] == '}' {
			return 0, 0, fmt.Errorf("no %s field found", pathOf(append(seen, keys[0])))
		}
		keyStart, keyEnd, err := c.str()
		if err != nil {
			return 0, 0, err
		}
		var key string
		if err := json.Unmarshal(c.b[keyStart:keyEnd], &key); err != nil {
			return 0, 0, fmt.Errorf("malformed object key: %w", err)
		}

		c.space()
		if c.i >= len(c.b) || c.b[c.i] != ':' {
			return 0, 0, fmt.Errorf("expected \":\" after key %q", key)
		}
		c.i++ // past ':'

		if key == keys[0] {
			return c.find(keys[1:], append(seen, key))
		}
		if err := c.skip(); err != nil {
			return 0, 0, err
		}
		c.space()
		if c.i < len(c.b) && c.b[c.i] == ',' {
			c.i++
			continue
		}
		if c.i < len(c.b) && c.b[c.i] == '}' {
			return 0, 0, fmt.Errorf("no %s field found", pathOf(append(seen, keys[0])))
		}
		return 0, 0, fmt.Errorf("expected \",\" or \"}\" after the value of %q", key)
	}
}

// skip advances the cursor past exactly one complete value.
func (c *jsonCursor) skip() error {
	c.space()
	if c.i >= len(c.b) {
		return fmt.Errorf("unexpected end of document")
	}
	switch c.b[c.i] {
	case '"':
		_, _, err := c.str()
		return err
	case '{', '[':
		// Containers are skipped by depth, with strings consumed whole so a
		// brace inside a string cannot move the depth counter.
		depth := 0
		for c.i < len(c.b) {
			switch c.b[c.i] {
			case '"':
				if _, _, err := c.str(); err != nil {
					return err
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					c.i++
					return nil
				}
			}
			c.i++
		}
		return fmt.Errorf("unterminated object or array")
	default:
		// A scalar: number, true, false or null. It ends where the enclosing
		// structure resumes.
		for c.i < len(c.b) && !strings.ContainsRune(",}] \t\r\n", rune(c.b[c.i])) {
			c.i++
		}
		return nil
	}
}

// str consumes the string starting at the cursor and returns its span,
// including both quotes.
func (c *jsonCursor) str() (start, end int, err error) {
	if c.i >= len(c.b) || c.b[c.i] != '"' {
		return 0, 0, fmt.Errorf("expected a string")
	}
	start = c.i
	for j := c.i + 1; j < len(c.b); j++ {
		switch c.b[j] {
		case '\\':
			j++ // skip the escaped byte
		case '"':
			c.i = j + 1
			return start, c.i, nil
		}
	}
	return 0, 0, fmt.Errorf("unterminated string")
}

func (c *jsonCursor) space() {
	for c.i < len(c.b) {
		switch c.b[c.i] {
		case ' ', '\t', '\r', '\n':
			c.i++
		default:
			return
		}
	}
}

// walkDoc follows keys through a decoded document and returns the string it
// ends at. It is shared by the JSON and TOML kinds, which both decode into
// map[string]any.
func walkDoc(doc any, keys []string) (string, error) {
	var seen []string
	cur := doc
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", fmt.Errorf("%s is not a table, so it has no %q inside it", pathOf(seen), key)
		}
		next, ok := m[key]
		if !ok {
			return "", fmt.Errorf("no %s field found", pathOf(append(seen, key)))
		}
		seen = append(seen, key)
		cur = next
	}
	v, ok := cur.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string value", pathOf(seen))
	}
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s is empty", pathOf(seen))
	}
	return strings.TrimSpace(v), nil
}

// pathOf renders a walked key path for an error message. The empty path is the
// document itself.
func pathOf(seen []string) string {
	if len(seen) == 0 {
		return "the document"
	}
	return strconvQuote(strings.Join(seen, "."))
}

func strconvQuote(s string) string { return `"` + s + `"` }
