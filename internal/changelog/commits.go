package changelog

import (
	"regexp"
	"strings"
	"unicode"
)

// conventionalCommit matches "type(scope)!: subject". The type is letters only,
// the scope is anything up to the closing bracket, and the "!" before the colon
// is the breaking-change marker.
var conventionalCommit = regexp.MustCompile(`^([A-Za-z]+)(?:\(([^)]*)\))?(!)?:[ \t]*(.+)$`)

// commitKinds maps the conventional commit types that describe a change a user
// would notice onto the kind they belong under. A type that is absent is
// dropped: docs, test, chore, ci, build and style describe the work rather than
// the product, and so do this repository's own release commits, whose type is
// either "release" or a "chore(release)".
var commitKinds = map[string]Kind{
	"feat":     Added,
	"fix":      Fixed,
	"perf":     Changed,
	"refactor": Changed,
	"security": Security,
	"revert":   Removed,
}

// FromCommits derives draft entries from conventional commit subjects, newest
// last.
//
// This is the fallback for a release with no fragments, and it is deliberately
// lossy. Subjects that are not conventional commits, and types that describe
// work rather than a change a user would notice, are left out entirely: a
// changelog of "chore: bump deps" is worse than no changelog, because it
// teaches a reader that the file is not worth opening.
//
// A breaking marker moves the entry to Changed and prefixes its text with
// "Breaking: ", whatever type carried it. A user reading a release wants the
// thing that will break their build at the top of a section they read, not
// filed under whichever category the author reached for.
func FromCommits(subjects []string) []Entry {
	var entries []Entry
	for _, subject := range subjects {
		m := conventionalCommit.FindStringSubmatch(strings.TrimSpace(subject))
		if m == nil {
			continue
		}
		kind, ok := commitKinds[strings.ToLower(m[1])]
		if !ok {
			continue
		}
		text := upperFirst(strings.TrimSpace(m[4]))
		if m[3] == "!" {
			kind = Changed
			text = "Breaking: " + text
		}
		entries = append(entries, Entry{Kind: kind, Text: text})
	}
	return entries
}

// upperFirst upper-cases the first character of a subject, which is written
// lower-case by convention in a commit and read as a sentence in a changelog.
func upperFirst(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+len(string(r)):]
	}
	return s
}
