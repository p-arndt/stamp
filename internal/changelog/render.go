package changelog

import (
	"fmt"
	"strings"
	"time"
)

// fileHeader introduces a CHANGELOG.md that stamp is creating from scratch. It
// says which conventions the file follows, so a reader who has met either of
// them elsewhere knows what to expect, and a tool that parses the file has
// something to key on.
const fileHeader = `# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

`

// dateLayout is the ISO date Keep a Changelog asks for in a version heading.
const dateLayout = "2006-01-02"

// Render returns the changelog section for one release: a "## <version> -
// <date>" heading followed by a "### <Kind>" block per kind that has entries,
// each entry a "- " list item. Kinds with no entries produce nothing, and no
// entries at all produce the empty string, which is how a caller asks whether
// there is anything to write.
//
// The version is written bare rather than in brackets. Keep a Changelog's own
// examples link the version, and the brackets are the markdown reference-link
// syntax for that; without the link definitions they are just noise, and this
// project's previous generated changelogs did not use them either.
//
// The result ends in a single trailing newline, so sections concatenate
// cleanly.
func Render(version string, date time.Time, entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## %s - %s\n", version, date.Format(dateLayout))
	for _, kind := range Kinds() {
		var texts []string
		for _, e := range entries {
			if e.Kind == kind {
				texts = append(texts, e.Text)
			}
		}
		if len(texts) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n", kind.Heading())
		for _, text := range texts {
			fmt.Fprintf(&b, "- %s\n", indentContinuation(text))
		}
	}
	return b.String()
}

// indentContinuation keeps a multi-line entry inside its list item. A fragment
// is usually one line, but a change that needs a second paragraph should not
// silently break the list it is in.
func indentContinuation(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = "  " + strings.TrimLeft(lines[i], " \t")
	}
	return strings.Join(lines, "\n")
}

// Body strips the version heading off a rendered section, leaving the grouped
// list. It is what goes into the annotated tag, where the tag name already says
// which version this is and repeating it in the first line of the message would
// only push the content further down every UI that shows a tag.
func Body(section string) string {
	rest := section
	if strings.HasPrefix(rest, "## ") {
		_, rest, _ = strings.Cut(rest, "\n")
	}
	rest = strings.TrimLeft(rest, "\n")
	rest = strings.TrimRight(rest, "\n")
	if rest == "" {
		return ""
	}
	return rest + "\n"
}

// Insert splices section into the contents of a CHANGELOG.md, directly above
// the most recent version heading, and returns the new contents.
//
// Newest first is the convention every changelog reader expects, and inserting
// above the top heading keeps whatever preamble the file already carries,
// hand-written or not. An empty or blank file gets the standard header first;
// a file with no "## " heading at all is something stamp did not write, so the
// section is appended rather than pushed in front of prose it cannot read.
func Insert(data []byte, section string) []byte {
	if section == "" {
		return data
	}
	text := string(data)
	if strings.TrimSpace(text) == "" {
		return []byte(fileHeader + section)
	}
	i := versionHeading(text)
	if i < 0 {
		return []byte(appendBlock(text, section))
	}
	return []byte(appendBlock(text[:i], section) + "\n" + text[i:])
}

// versionHeading is the offset of the first "## " heading in text, or -1.
func versionHeading(text string) int {
	if strings.HasPrefix(text, "## ") {
		return 0
	}
	if i := strings.Index(text, "\n## "); i >= 0 {
		return i + 1
	}
	return -1
}

// appendBlock joins a block onto a prefix with exactly one blank line between
// them, whatever trailing whitespace the prefix happened to carry.
func appendBlock(prefix, block string) string {
	prefix = strings.TrimRight(prefix, "\n")
	if prefix == "" {
		return block
	}
	return prefix + "\n\n" + block
}
