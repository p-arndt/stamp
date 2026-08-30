package changelog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fragment writes a fragment file under dir, creating the directories it needs.
func fragment(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// texts is the entry texts in order, which is what most Read assertions are
// actually about.
func texts(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Text)
	}
	return out
}

// equal compares two string slices, since the assertions below are all small.
func equal(a, b []string) bool {
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

var day = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

func TestParseKind(t *testing.T) {
	tests := []struct {
		in   string
		want Kind
		ok   bool
	}{
		{"added", Added, true},
		{"Added", Added, true},
		{"SECURITY", Security, true},
		{"  fixed  ", Fixed, true},
		{"deprecated", Deprecated, true},
		{"removed", Removed, true},
		{"changed", Changed, true},
		{"add", "", false},
		{"", "", false},
		{"feature", "", false},
	}
	for _, tt := range tests {
		got, err := ParseKind(tt.in)
		if tt.ok {
			if err != nil {
				t.Errorf("ParseKind(%q): %v", tt.in, err)
				continue
			}
			if got != tt.want {
				t.Errorf("ParseKind(%q) = %q, want %q", tt.in, got, tt.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("ParseKind(%q) = %q, want an error", tt.in, got)
		}
	}
}

// A user who mistyped a kind gets the list of them, so the message has to carry
// every one.
func TestParseKindErrorNamesEveryKind(t *testing.T) {
	_, err := ParseKind("add")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, k := range Kinds() {
		if !strings.Contains(err.Error(), string(k)) {
			t.Errorf("error %q does not name %q", err, k)
		}
	}
	if !strings.Contains(err.Error(), `"add"`) {
		t.Errorf("error %q does not quote what was typed", err)
	}
}

func TestKindsOrderAndHeading(t *testing.T) {
	want := []string{"added", "changed", "deprecated", "removed", "fixed", "security"}
	var got []string
	for _, k := range Kinds() {
		got = append(got, string(k))
	}
	if !equal(got, want) {
		t.Errorf("Kinds() = %v, want %v", got, want)
	}
	if Added.Heading() != "Added" || Security.Heading() != "Security" {
		t.Errorf("Heading = %q/%q", Added.Heading(), Security.Heading())
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Add a flag", "add-a-flag"},
		{"punctuation", "Pre-releases: `stamp prerelease minor` opens a beta series.", "pre-releases-stamp-prerelease-minor-opens"},
		{"digits kept", "Bump Go to 1.26", "bump-go-to-1-26"},
		{"trims separators", "  --- hello --- ", "hello"},
		{"caps lowered", "TAG CHECK Fixed", "tag-check-fixed"},
		{"at most six words", "one two three four five six seven eight", "one-two-three-four-five-six"},
		{"unicode separates", "Größe geändert", "gr-e-ge-ndert"},
		{"nothing to slug", "??? — !!!", fallbackSlug},
		{"empty", "", fallbackSlug},
		{"non-ascii only", "変更", fallbackSlug},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Slug(tt.in); got != tt.want {
				t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A directory that was never created is the state of every repository before
// the first note, and must read as "nothing yet" rather than as a failure.
func TestReadMissingDirectory(t *testing.T) {
	root := t.TempDir()
	entries, err := Read(root, filepath.Join(root, ".stamp", "changelog"), "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Read = %v, want nothing", entries)
	}
}

func TestReadSortsByKindThenFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")
	fragment(t, dir, "zebra.added.md", "Zebra\n")
	fragment(t, dir, "apple.added.md", "Apple\n")
	fragment(t, dir, "remote-check.fixed.md", "  The tag check no longer passes when the remote is unreachable.  \n")
	fragment(t, dir, "old-flag.removed.md", "Old flag\n")

	entries, err := Read(root, dir, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"Apple", "Zebra", "Old flag", "The tag check no longer passes when the remote is unreachable."}
	if got := texts(entries); !equal(got, want) {
		t.Fatalf("Read = %v, want %v", got, want)
	}
	if entries[0].Kind != Added || entries[2].Kind != Removed || entries[3].Kind != Fixed {
		t.Errorf("kinds = %q %q %q", entries[0].Kind, entries[2].Kind, entries[3].Kind)
	}
	if want := filepath.ToSlash(filepath.Join(".stamp", "changelog", "apple.added.md")); entries[0].File != want {
		t.Errorf("File = %q, want %q", entries[0].File, want)
	}
}

// Files that are not fragments share the directory with them and must be left
// alone: a README explaining the convention is the obvious one.
func TestReadIgnoresNonFragments(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")
	fragment(t, dir, "real.added.md", "Real\n")
	fragment(t, dir, "README.md", "How to write a note.\n")
	fragment(t, dir, ".gitkeep", "")
	fragment(t, dir, "notes.txt", "not a fragment")
	fragment(t, dir, "web/scoped.added.md", "Scoped\n")

	entries, err := Read(root, dir, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := texts(entries); !equal(got, []string{"Real"}) {
		t.Errorf("Read = %v, want [Real]", got)
	}
}

func TestReadComponent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")
	fragment(t, dir, "web/login.fixed.md", "Login works again\n")
	fragment(t, dir, "api/rate-limit.added.md", "Rate limiting\n")

	entries, err := Read(root, dir, "web")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := texts(entries); !equal(got, []string{"Login works again"}) {
		t.Fatalf("Read(web) = %v", got)
	}
	if want := ".stamp/changelog/web/login.fixed.md"; entries[0].File != want {
		t.Errorf("File = %q, want %q", entries[0].File, want)
	}
}

// A typo'd kind must stop the release rather than drop the change silently.
func TestReadUnknownKindIsAnError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")
	fragment(t, dir, "typo.add.md", "Something\n")

	_, err := Read(root, dir, "")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "typo.add.md") {
		t.Errorf("error %q does not name the file", err)
	}
	for _, k := range Kinds() {
		if !strings.Contains(err.Error(), string(k)) {
			t.Errorf("error %q does not name %q", err, k)
		}
	}
}

func TestReadEmptyFragmentIsAnError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")
	fragment(t, dir, "blank.added.md", "   \n\n")

	_, err := Read(root, dir, "")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "blank.added.md") {
		t.Errorf("error %q does not name the file", err)
	}
}

func TestWrite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")

	path, err := Write(root, dir, "", Added, "Pre-releases: `stamp prerelease minor` opens a beta series.")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := ".stamp/changelog/pre-releases-stamp-prerelease-minor-opens.added.md"
	if path != want {
		t.Fatalf("Write = %q, want %q", path, want)
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "Pre-releases: `stamp prerelease minor` opens a beta series.\n" {
		t.Errorf("body = %q", got)
	}
}

// Noting the same thing twice must never overwrite the first note.
func TestWriteCollision(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")
	var paths []string
	for range 3 {
		path, err := Write(root, dir, "", Fixed, "Tag check")
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		paths = append(paths, path)
	}
	want := []string{
		".stamp/changelog/tag-check.fixed.md",
		".stamp/changelog/tag-check-2.fixed.md",
		".stamp/changelog/tag-check-3.fixed.md",
	}
	if !equal(paths, want) {
		t.Errorf("Write = %v, want %v", paths, want)
	}
}

func TestWriteComponentAndRoundTrip(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")
	path, err := Write(root, dir, "web", Changed, "  Login form redesigned  ")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if path != ".stamp/changelog/web/login-form-redesigned.changed.md" {
		t.Fatalf("Write = %q", path)
	}
	entries, err := Read(root, dir, "web")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := texts(entries); !equal(got, []string{"Login form redesigned"}) {
		t.Errorf("Read = %v", got)
	}
}

func TestWriteRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")
	if _, err := Write(root, dir, "", Added, "   "); err == nil {
		t.Error("empty text: want an error")
	}
	if _, err := Write(root, dir, "", Kind("nope"), "Something"); err == nil {
		t.Error("unknown kind: want an error")
	}
}

func TestRender(t *testing.T) {
	tests := []struct {
		name    string
		entries []Entry
		want    string
	}{
		{"nothing", nil, ""},
		{
			"grouped in kind order",
			[]Entry{
				{Kind: Fixed, Text: "The tag check no longer passes when the remote is unreachable."},
				{Kind: Added, Text: "Pre-releases: `stamp prerelease minor` opens a beta series."},
				{Kind: Added, Text: "A second addition."},
			},
			"## 1.3.0 - 2026-08-31\n" +
				"\n### Added\n\n" +
				"- Pre-releases: `stamp prerelease minor` opens a beta series.\n" +
				"- A second addition.\n" +
				"\n### Fixed\n\n" +
				"- The tag check no longer passes when the remote is unreachable.\n",
		},
		{
			"every kind",
			[]Entry{
				{Kind: Security, Text: "S"},
				{Kind: Fixed, Text: "F"},
				{Kind: Removed, Text: "R"},
				{Kind: Deprecated, Text: "D"},
				{Kind: Changed, Text: "C"},
				{Kind: Added, Text: "A"},
			},
			"## 1.3.0 - 2026-08-31\n" +
				"\n### Added\n\n- A\n" +
				"\n### Changed\n\n- C\n" +
				"\n### Deprecated\n\n- D\n" +
				"\n### Removed\n\n- R\n" +
				"\n### Fixed\n\n- F\n" +
				"\n### Security\n\n- S\n",
		},
		{
			"multi-line entry stays in its item",
			[]Entry{{Kind: Added, Text: "First line\nsecond line"}},
			"## 1.3.0 - 2026-08-31\n\n### Added\n\n- First line\n  second line\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Render("1.3.0", day, tt.entries)
			if got != tt.want {
				t.Errorf("Render =\n%q\nwant\n%q", got, tt.want)
			}
			if got != "" && !strings.HasSuffix(got, "\n") {
				t.Error("section does not end in a newline")
			}
			if strings.HasSuffix(got, "\n\n") {
				t.Error("section ends in a blank line")
			}
		})
	}
}

func TestBody(t *testing.T) {
	section := Render("1.3.0", day, []Entry{
		{Kind: Added, Text: "A"},
		{Kind: Fixed, Text: "F"},
	})
	want := "### Added\n\n- A\n\n### Fixed\n\n- F\n"
	if got := Body(section); got != want {
		t.Errorf("Body =\n%q\nwant\n%q", got, want)
	}
	if got := Body(""); got != "" {
		t.Errorf("Body(\"\") = %q", got)
	}
	if got := Body("## 1.3.0 - 2026-08-31\n"); got != "" {
		t.Errorf("Body(heading only) = %q", got)
	}
	// Text that never carried a heading comes back unchanged but for its
	// trailing whitespace.
	if got := Body("- A\n\n\n"); got != "- A\n" {
		t.Errorf("Body(no heading) = %q", got)
	}
}

func TestInsert(t *testing.T) {
	section := "## 1.3.0 - 2026-08-31\n\n### Added\n\n- New thing\n"

	t.Run("empty file gets the header", func(t *testing.T) {
		got := string(Insert(nil, section))
		if !strings.HasPrefix(got, "# Changelog\n") {
			t.Fatalf("no header:\n%s", got)
		}
		if !strings.HasSuffix(got, section) {
			t.Fatalf("section is not at the end:\n%s", got)
		}
	})

	t.Run("blank file gets the header", func(t *testing.T) {
		got := string(Insert([]byte("\n  \n"), section))
		if !strings.HasPrefix(got, "# Changelog\n") || !strings.HasSuffix(got, section) {
			t.Fatalf("got:\n%s", got)
		}
	})

	t.Run("inserted above the newest version", func(t *testing.T) {
		before := "# Changelog\n\nPreamble.\n\n## 1.2.0 - 2026-01-01\n\n### Added\n\n- Old thing\n"
		want := "# Changelog\n\nPreamble.\n\n" + section + "\n## 1.2.0 - 2026-01-01\n\n### Added\n\n- Old thing\n"
		if got := string(Insert([]byte(before), section)); got != want {
			t.Errorf("Insert =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("header is not duplicated", func(t *testing.T) {
		first := string(Insert(nil, section))
		second := string(Insert([]byte(first), "## 1.4.0 - 2026-09-01\n\n### Fixed\n\n- A fix\n"))
		if n := strings.Count(second, "# Changelog"); n != 1 {
			t.Fatalf("header appears %d times:\n%s", n, second)
		}
		if strings.Index(second, "1.4.0") > strings.Index(second, "1.3.0") {
			t.Errorf("the new section is not on top:\n%s", second)
		}
	})

	t.Run("file starting with a version heading", func(t *testing.T) {
		before := "## 1.2.0 - 2026-01-01\n\n- Old\n"
		want := section + "\n" + before
		if got := string(Insert([]byte(before), section)); got != want {
			t.Errorf("Insert =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("file without any heading is appended to", func(t *testing.T) {
		before := "Some notes someone kept by hand.\n"
		want := "Some notes someone kept by hand.\n\n" + section
		if got := string(Insert([]byte(before), section)); got != want {
			t.Errorf("Insert =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("nothing to insert leaves the file alone", func(t *testing.T) {
		before := []byte("# Changelog\n")
		if got := string(Insert(before, "")); got != string(before) {
			t.Errorf("Insert = %q", got)
		}
	})
}

func TestFromCommits(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		kind    Kind
		text    string
		dropped bool
	}{
		{name: "feat", subject: "feat: add a flag", kind: Added, text: "Add a flag"},
		{name: "feat with scope", subject: "feat(cli): add a flag", kind: Added, text: "Add a flag"},
		{name: "fix", subject: "fix: stop panicking on a detached head", kind: Fixed, text: "Stop panicking on a detached head"},
		{name: "perf", subject: "perf: cache the tag lookup", kind: Changed, text: "Cache the tag lookup"},
		{name: "refactor", subject: "refactor: split the release runner", kind: Changed, text: "Split the release runner"},
		{name: "security", subject: "security: verify the checksum", kind: Security, text: "Verify the checksum"},
		{name: "revert", subject: "revert: the beta series flag", kind: Removed, text: "The beta series flag"},
		{name: "breaking", subject: "feat!: drop the old config", kind: Changed, text: "Breaking: Drop the old config"},
		{name: "breaking with scope", subject: "fix(config)!: rename the key", kind: Changed, text: "Breaking: Rename the key"},
		{name: "uppercase type", subject: "FEAT: shout", kind: Added, text: "Shout"},
		{name: "docs", subject: "docs: rewrite the readme", dropped: true},
		{name: "test", subject: "test: cover the parser", dropped: true},
		{name: "chore", subject: "chore: bump deps", dropped: true},
		{name: "ci", subject: "ci: pin the runner", dropped: true},
		{name: "build", subject: "build: update the makefile", dropped: true},
		{name: "style", subject: "style: gofmt", dropped: true},
		{name: "release commit", subject: "release: v1.2.3", dropped: true},
		{name: "chore release commit", subject: "chore(release): v1.2.3", dropped: true},
		{name: "not conventional", subject: "Fixed a thing", dropped: true},
		{name: "no subject", subject: "feat:", dropped: true},
		{name: "empty", subject: "", dropped: true},
		{name: "colon in prose only", subject: "the thing: it broke", dropped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromCommits([]string{tt.subject})
			if tt.dropped {
				if len(got) != 0 {
					t.Fatalf("FromCommits(%q) = %v, want nothing", tt.subject, got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("FromCommits(%q) = %v, want one entry", tt.subject, got)
			}
			if got[0].Kind != tt.kind || got[0].Text != tt.text {
				t.Errorf("FromCommits(%q) = %q/%q, want %q/%q", tt.subject, got[0].Kind, got[0].Text, tt.kind, tt.text)
			}
			if got[0].File != "" {
				t.Errorf("a commit entry has File = %q", got[0].File)
			}
		})
	}
}

// The subjects arrive newest last and the entries must keep that order, so the
// rendered list reads oldest to newest within its kind.
func TestFromCommitsKeepsOrder(t *testing.T) {
	got := FromCommits([]string{"feat: one", "chore: skipped", "feat: two", "fix: three"})
	if !equal(texts(got), []string{"One", "Two", "Three"}) {
		t.Errorf("FromCommits = %v", texts(got))
	}
}

// The whole point of the two halves meeting: fragments in, a section out, and a
// tag body that is the same thing without the heading.
func TestEndToEnd(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stamp", "changelog")
	if _, err := Write(root, dir, "", Added, "Pre-releases: `stamp prerelease minor` opens a beta series."); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, dir, "", Fixed, "The tag check no longer passes when the remote is unreachable."); err != nil {
		t.Fatal(err)
	}
	entries, err := Read(root, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	section := Render("1.3.0", day, entries)
	want := "## 1.3.0 - 2026-08-31\n" +
		"\n### Added\n\n- Pre-releases: `stamp prerelease minor` opens a beta series.\n" +
		"\n### Fixed\n\n- The tag check no longer passes when the remote is unreachable.\n"
	if section != want {
		t.Fatalf("Render =\n%q\nwant\n%q", section, want)
	}
	if body := Body(section); strings.Contains(body, "1.3.0") {
		t.Errorf("the tag body repeats the version:\n%s", body)
	}
	file := Insert(nil, section)
	if !strings.Contains(string(file), want) {
		t.Errorf("the section is not in the file:\n%s", file)
	}
}
