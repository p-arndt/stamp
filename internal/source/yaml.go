package source

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlSource is a field inside a YAML document: Chart.yaml#appVersion.
type yamlSource struct{ base }

func (s *yamlSource) Read() (string, error) {
	node, err := s.node()
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(node.Value)
	if v == "" {
		return "", fmt.Errorf("%s: %s is empty", s.rel, pathOf(s.keys()))
	}
	return v, nil
}

// Write replaces the scalar in place, keeping its quoting style, so the rest of
// the document (comments, anchors, indentation) is untouched.
func (s *yamlSource) Write(version string) error {
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return err
	}
	node, err := s.node()
	if err != nil {
		return err
	}
	start, end, err := scalarSpan(b, node)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", s.rel, pathOf(s.keys()), err)
	}
	return writeKeepingMode(s.abs(), splice(b, start, end, []byte(renderYAMLScalar(version, node.Style))))
}

// node walks to the scalar the field path names.
func (s *yamlSource) node() (*yaml.Node, error) {
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid YAML: %w", s.rel, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s is empty", s.rel)
	}

	cur := doc.Content[0]
	var seen []string
	for _, key := range s.keys() {
		if cur.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: %s is not a mapping, so it has no %q inside it", s.rel, pathOf(seen), key)
		}
		next := mappingValue(cur, key)
		if next == nil {
			return nil, fmt.Errorf("%s: no %s field found", s.rel, pathOf(append(seen, key)))
		}
		seen = append(seen, key)
		cur = next
	}
	if cur.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("%s: %s is not a scalar value", s.rel, pathOf(seen))
	}
	return cur, nil
}

// mappingValue returns the value node for key, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// scalarSpan finds the byte span of a scalar node in the original bytes, from
// the line and column the parser recorded.
//
// The span is recovered rather than re-marshalled because re-marshalling the
// document would drop every comment in it and re-indent everything else. What
// it cannot recover it refuses: a block scalar (| or >) is not a place a
// version literal belongs, and guessing at its extent would risk corrupting the
// file.
func scalarSpan(b []byte, node *yaml.Node) (start, end int, err error) {
	switch node.Style {
	case yaml.LiteralStyle, yaml.FoldedStyle:
		return 0, 0, fmt.Errorf("is a block scalar (| or >); stamp only rewrites a plain or quoted value")
	}

	start, err = offsetOf(b, node.Line, node.Column)
	if err != nil {
		return 0, 0, err
	}

	switch b[start] {
	case '"':
		end, err = closingQuote(b, start, true)
	case '\'':
		end, err = closingQuote(b, start, false)
	default:
		end = plainScalarEnd(b, start)
	}
	if err != nil {
		return 0, 0, err
	}

	// The safety net: what was cut out has to be what the parser read. If the
	// two disagree, the position was not where we think it was, and writing
	// there would corrupt the file.
	if got := unquoteYAML(string(b[start:end])); got != node.Value {
		return 0, 0, fmt.Errorf("cannot locate the value in the file (found %q, expected %q)", got, node.Value)
	}
	return start, end, nil
}

// offsetOf turns a 1-based line and column into a byte offset.
func offsetOf(b []byte, line, column int) (int, error) {
	if line < 1 || column < 1 {
		return 0, fmt.Errorf("no position recorded for the value")
	}
	offset, current := 0, 1
	for current < line {
		nl := indexByteFrom(b, offset, '\n')
		if nl < 0 {
			return 0, fmt.Errorf("line %d is past the end of the file", line)
		}
		offset = nl + 1
		current++
	}
	offset += column - 1
	if offset >= len(b) {
		return 0, fmt.Errorf("line %d column %d is past the end of the file", line, column)
	}
	return offset, nil
}

func indexByteFrom(b []byte, from int, c byte) int {
	for i := from; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// closingQuote returns the offset just past the closing quote of the string
// starting at start. In a double-quoted YAML scalar a backslash escapes; in a
// single-quoted one only ” does.
func closingQuote(b []byte, start int, double bool) (int, error) {
	quote := b[start]
	for i := start + 1; i < len(b); i++ {
		if double && b[i] == '\\' {
			i++
			continue
		}
		if b[i] != quote {
			continue
		}
		if !double && i+1 < len(b) && b[i+1] == '\'' {
			i++ // '' is an escaped single quote
			continue
		}
		return i + 1, nil
	}
	return 0, fmt.Errorf("unterminated quoted value")
}

// plainScalarEnd returns the offset just past an unquoted scalar: the end of
// the line, minus a trailing comment and any trailing whitespace.
func plainScalarEnd(b []byte, start int) int {
	end := len(b)
	if nl := indexByteFrom(b, start, '\n'); nl >= 0 {
		end = nl
	}
	// " #" starts a comment; a "#" glued to the value is part of it.
	for i := start; i < end-1; i++ {
		if (b[i] == ' ' || b[i] == '\t') && b[i+1] == '#' {
			end = i
			break
		}
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r') {
		end--
	}
	return end
}

// unquoteYAML reads a scalar token back into its value, for the verification in
// scalarSpan. Only the escapes a version literal could plausibly carry are
// handled; anything else falls through and the comparison fails, which is the
// safe outcome.
func unquoteYAML(token string) string {
	switch {
	case strings.HasPrefix(token, `"`) && strings.HasSuffix(token, `"`) && len(token) >= 2:
		var out string
		if err := yaml.Unmarshal([]byte(token), &out); err == nil {
			return out
		}
		return token
	case strings.HasPrefix(token, `'`) && strings.HasSuffix(token, `'`) && len(token) >= 2:
		return strings.ReplaceAll(token[1:len(token)-1], `''`, `'`)
	default:
		return token
	}
}

// renderYAMLScalar writes version back in the style it replaces, so a quoted
// value stays quoted and the diff is the version alone.
func renderYAMLScalar(version string, style yaml.Style) string {
	switch style {
	case yaml.DoubleQuotedStyle:
		return `"` + version + `"`
	case yaml.SingleQuotedStyle:
		return `'` + version + `'`
	default:
		return version
	}
}
