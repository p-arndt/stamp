// Package changelog handles human-written changelog entries: the small markdown
// files a developer drops in during a change, and the release section they are
// rendered into.
//
// The point of writing them by hand is that a changelog is for users, and a
// commit history is for developers. "fix: nil deref in walk" is true and
// useless; "Releasing from a detached HEAD no longer panics" is what belongs in
// a release note. Collecting one file per change also keeps the entries out of
// the merge conflicts a shared CHANGELOG.md would produce, which is the reason
// towncrier and changesets both work this way.
//
// A fragment is a plain markdown file whose name carries the metadata and whose
// body is nothing but the prose a user reads:
//
//	<dir>/<slug>.<kind>.md              a repository without components
//	<dir>/<component>/<slug>.<kind>.md  a repository with components
//
// There is deliberately no frontmatter. The kind is the only metadata there is,
// a file name holds it perfectly well, and a fragment stays something anyone can
// write in an editor without looking up a syntax.
//
// The package is pure: it takes paths and strings, touches no git and reads no
// config. Everything to do with where the directory is, and what to do with the
// rendered section afterwards, belongs to the caller.
package changelog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Kind is one of the six Keep a Changelog categories.
type Kind string

// The kinds, spelled as they appear in a fragment's file name.
const (
	Added      Kind = "added"
	Changed    Kind = "changed"
	Deprecated Kind = "deprecated"
	Removed    Kind = "removed"
	Fixed      Kind = "fixed"
	Security   Kind = "security"
)

// Kinds lists the kinds in the order sections are rendered, which is Keep a
// Changelog's own order rather than an alphabetical one: what was gained, then
// what moved, then what went, then what was repaired.
func Kinds() []Kind {
	return []Kind{Added, Changed, Deprecated, Removed, Fixed, Security}
}

// ParseKind reads a kind name, case-insensitively, so `stamp note Added` and
// `stamp note added` mean the same thing.
//
// The error names every valid kind because it is what a user who mistyped one
// sees, and at that moment the list is the only thing worth saying.
func ParseKind(s string) (Kind, error) {
	k := Kind(strings.ToLower(strings.TrimSpace(s)))
	for _, valid := range Kinds() {
		if k == valid {
			return k, nil
		}
	}
	return "", fmt.Errorf("unknown change kind %q; use one of %s", s, kindList())
}

// kindList renders the kinds for an error message.
func kindList() string {
	names := make([]string, 0, len(Kinds()))
	for _, k := range Kinds() {
		names = append(names, string(k))
	}
	return strings.Join(names, ", ")
}

// Heading is the kind as it appears in a rendered "### Added" heading.
func (k Kind) Heading() string {
	if k == "" {
		return ""
	}
	return strings.ToUpper(string(k[:1])) + string(k[1:])
}

// order is a kind's position in Kinds, used for sorting.
func (k Kind) order() int {
	for i, valid := range Kinds() {
		if k == valid {
			return i
		}
	}
	return len(Kinds())
}

// Entry is one user-facing change.
type Entry struct {
	Kind Kind
	// Text is the entry as a user reads it, markdown, usually one line.
	Text string
	// File is the repository-relative path of the fragment it came from, and
	// is empty for an entry derived from a commit.
	File string
}

// File permissions for what Write creates. Fragments are ordinary source files
// that get committed, so they take the ordinary modes.
const (
	dirMode  = 0o755
	fileMode = 0o644
)

// Read collects the fragments belonging to component from dir, an absolute
// path. component is empty in a repository that declares none, and otherwise
// selects the subdirectory of the same name; a component reads its own
// fragments and nobody else's.
//
// A missing dir is not an error: it means nothing has been noted yet, which is
// the state every repository starts in and most releases of a small change end
// in.
//
// root is the absolute repository root, used to make Entry.File relative to it,
// because the paths are shown to a user and staged with git, and both want them
// relative.
//
// Entries come back sorted by kind, in Kinds order, and then by file name, so
// the rendered section does not reshuffle itself between runs on machines whose
// directory order differs.
func Read(root, dir, component string) ([]Entry, error) {
	if component != "" {
		dir = filepath.Join(dir, component)
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read changelog fragments in %s: %w", dir, err)
	}

	var entries []Entry
	for _, name := range names {
		if name.IsDir() {
			// A component's subdirectory, read only when it is asked for.
			continue
		}
		kind, ok, err := kindOfFragment(name.Name())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		path := filepath.Join(dir, name.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read changelog fragment %s: %w", path, err)
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			return nil, fmt.Errorf("changelog fragment %s is empty; write the change as a user would read it, or delete the file", rel(root, path))
		}
		entries = append(entries, Entry{Kind: kind, Text: text, File: rel(root, path)})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if a, b := entries[i].Kind.order(), entries[j].Kind.order(); a != b {
			return a < b
		}
		return entries[i].File < entries[j].File
	})
	return entries, nil
}

// kindOfFragment reads the kind out of a fragment's file name. ok is false for
// a file that is not a fragment at all.
//
// The shape is "<slug>.<kind>.md", so a .md file whose stem carries no dot is
// simply not a fragment: a README, a template or a notes file may live in the
// directory undisturbed. A stem that *does* carry a dot is a fragment with a
// kind that failed to parse, and that is an error rather than a silent skip,
// because "added.md" spelled "add" would otherwise drop the change on the floor
// at exactly the moment nobody is looking.
func kindOfFragment(name string) (Kind, bool, error) {
	if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
		return "", false, nil
	}
	stem := strings.TrimSuffix(name, ".md")
	slug, kindName, hasKind := lastCut(stem, ".")
	if !hasKind || slug == "" {
		return "", false, nil
	}
	kind, err := ParseKind(kindName)
	if err != nil {
		return "", false, fmt.Errorf("changelog fragment %s has an unknown kind %q; name it <slug>.<kind>.md with one of %s", name, kindName, kindList())
	}
	return kind, true, nil
}

// lastCut is strings.Cut around the final separator rather than the first.
func lastCut(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// rel makes an absolute path relative to root, falling back to the absolute
// path when it cannot, since the value is for display and a failure here is not
// worth an error of its own.
func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

// Write records a new fragment and returns its repository-relative path.
//
// The file name is derived from the text, so a directory of fragments reads as
// a list of pending changes without opening any of them. If that name is taken
// the next free "-2", "-3" and so on is used: noting the same thing twice is
// usually a mistake, but silently overwriting the first note would be a worse
// answer to it than keeping both.
func Write(root, dir, component string, k Kind, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("a changelog note needs some text")
	}
	if _, err := ParseKind(string(k)); err != nil {
		return "", err
	}
	if component != "" {
		dir = filepath.Join(dir, component)
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("create changelog directory %s: %w", dir, err)
	}

	stem := Slug(text)
	path := filepath.Join(dir, fmt.Sprintf("%s.%s.md", stem, k))
	for n := 2; exists(path); n++ {
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.%s.md", stem, n, k))
	}
	if err := os.WriteFile(path, []byte(text+"\n"), fileMode); err != nil {
		return "", fmt.Errorf("write changelog fragment %s: %w", path, err)
	}
	return rel(root, path), nil
}

// exists reports whether anything is at path.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// maxSlugWords caps a file name at something still readable in a directory
// listing. Six words is enough to tell two notes apart, which is all the name
// has to do; the file itself holds the sentence.
const maxSlugWords = 6

// fallbackSlug names a fragment whose text slugs to nothing, which happens for
// a note written entirely in a non-ASCII script, or in punctuation.
const fallbackSlug = "note"

// Slug turns entry text into a file-name stem: lowercase, ASCII letters, digits
// and dashes, at most six words, never empty.
//
// Anything outside ASCII alphanumerics separates words rather than being
// transliterated. A file name is an identifier here, not content, and a rough
// stem plus the fragment's actual text beats carrying a transliteration table
// that would be wrong for half the languages it met.
func Slug(text string) string {
	var words []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			word.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			word.WriteRune(r + ('a' - 'A'))
		default:
			flush()
		}
		if len(words) == maxSlugWords {
			break
		}
	}
	flush()
	if len(words) == 0 {
		return fallbackSlug
	}
	return strings.Join(words, "-")
}
