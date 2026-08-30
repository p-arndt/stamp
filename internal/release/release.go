// Package release carries out a release: preflight, write, commit, tag, push.
//
// The order is deliberate: every check that can be made runs *before* the
// first file is written. That way the common failure (dirty tree, wrong branch,
// tag already taken) never leaves anything to undo. What happens after the
// first write is undone stage by stage, back to the last state that is
// internally consistent; see rollback below.
package release

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/p-arndt/stamp/internal/changelog"
	"github.com/p-arndt/stamp/internal/config"
	"github.com/p-arndt/stamp/internal/gitx"
	"github.com/p-arndt/stamp/internal/ui"
	"github.com/p-arndt/stamp/internal/version"
)

// Options are the flags of `stamp release`.
type Options struct {
	// Arg is the bump keyword or explicit version.
	Arg string
	// DryRun shows the plan and stops.
	DryRun bool
	// NoPush writes, commits and tags, but does not push.
	NoPush bool
	// Yes skips the confirmation prompt.
	Yes bool
	// NoFetch skips the network round-trip that verifies the branch is up to
	// date and the tag is free on the remote.
	NoFetch bool
	// Branch overrides the configured release branch for this run.
	Branch string
	// Pre cuts a pre-release: Arg is read as the bump the coming stable
	// release stands for, and PreID names the series.
	Pre bool
	// PreID overrides the configured pre-release identifier for this run.
	PreID string
	// Edit opens the rendered changelog section in an editor before it is
	// committed.
	Edit bool
}

// Plan is everything stamp resolved before touching the repository.
type Plan struct {
	Config *config.Config
	// Comp is the component being released. A repository that declares none
	// has exactly one, and this is it.
	Comp    *config.Component
	Repo    *gitx.Repo
	Options Options

	Current string
	Next    string
	Tag     string
	Commit  string
	Branch  string
	Remote  string

	// Unchanged is true when the version files already hold the target
	// version. That is the first-release case: there is nothing to write and
	// nothing to commit, so stamp tags the current HEAD. A changelog section
	// can still exist, and is still written and committed.
	Unchanged bool

	// Notes are the changelog entries this release publishes, empty when the
	// changelog is not in use.
	Notes []changelog.Entry
	// NotesFrom says where Notes came from: "fragments", "commits", or "".
	NotesFrom string
	// Section is the rendered changelog section, empty when there is nothing
	// to say. `--edit` replaces it between Prepare and Run.
	Section string

	// notesSince is the tag the drafted entries were taken since, for the
	// preflight detail. It is empty unless NotesFrom is FromCommits.
	notesSince string

	checks []check
}

// Where Notes came from, as reported by NotesFrom.
const (
	FromFragments = "fragments"
	FromCommits   = "commits"
)

type check struct {
	label   string
	ok      bool
	skipped bool
	detail  string
}

// Prepare resolves the plan and runs every preflight check. It returns a plan
// even when checks failed, so the caller can print the full list.
func Prepare(cfg *config.Config, comp *config.Component, repo *gitx.Repo, opts Options) (*Plan, error) {
	p := &Plan{Config: cfg, Comp: comp, Repo: repo, Options: opts, Remote: comp.Remote}

	current, err := comp.Source().Read()
	if err != nil {
		return nil, fmt.Errorf("reading the current version: %w", err)
	}
	p.Current = current

	next, err := resolve(comp, opts, current)
	if err != nil {
		return nil, err
	}
	p.Next = next
	p.Tag = comp.RenderTag(next)
	p.Commit = comp.RenderCommit(next, p.Tag)

	wantBranch := comp.Branch
	if opts.Branch != "" {
		wantBranch = opts.Branch
	}
	p.Branch = wantBranch

	// The changelog is resolved before preflight, because one of the checks is
	// about what it found. A fragment that will not read is a hard error rather
	// than a failed check: it means a note somebody wrote would be dropped, and
	// no amount of "release anyway" is the right answer to that.
	if comp.ChangelogEnabled(repo.Root) {
		notes, from, since, err := Notes(comp, repo)
		if err != nil {
			return nil, err
		}
		p.Notes, p.NotesFrom, p.notesSince = notes, from, since
		p.Section = changelog.Render(next, time.Now(), notes)
	}

	if err := p.preflight(); err != nil {
		return p, err
	}
	return p, nil
}

// resolve turns the CLI argument into the target version, as a plain bump or,
// for `stamp prerelease`, as the next pre-release of that bump.
//
// Where the identifier comes from, in order: --type, then the series the
// current version is already in, then the configured default. The middle step
// is what keeps a series walkable. After `--type rc` put the project on
// 1.3.0-rc.1, a bare `stamp prerelease` has to continue at rc.2 rather than
// drop back to the configured beta, which would be *lower* in semver and would
// die in preflight. It applies to the bare form only: a bump opens a series for
// a different release, and that one starts at the configured identifier again.
func resolve(comp *config.Component, opts Options, current string) (string, error) {
	if !opts.Pre {
		return version.Resolve(current, opts.Arg)
	}
	id := opts.PreID
	if id == "" && version.ContinuesSeries(current, opts.Arg) {
		id = version.PreIDOf(current)
	}
	if id == "" {
		id = comp.PreID
	}
	return version.ResolvePre(current, opts.Arg, id)
}

// Notes collects the changelog entries a release of comp would publish right
// now: the fragments noted since the last release, or, when there are none and
// the component's fallback allows it, drafts from the commits since its last
// tag. from is FromFragments, FromCommits or "" and since names the tag the
// drafts were taken after, for the output that has to tell a user which of the
// two they are looking at.
//
// It is exported because `stamp changelog` shows exactly what a release would
// publish, and the only way to promise that is to run the same code.
func Notes(comp *config.Component, repo *gitx.Repo) (entries []changelog.Entry, from, since string, err error) {
	if dir := comp.Changelog.Dir; dir != "" {
		abs := filepath.Join(repo.Root, filepath.FromSlash(dir))
		entries, err = changelog.Read(repo.Root, abs, comp.Name)
		if err != nil {
			return nil, "", "", err
		}
	}
	if len(entries) > 0 {
		return entries, FromFragments, "", nil
	}
	if comp.Changelog.Fallback != config.FallbackCommits {
		return nil, "", "", nil
	}
	// The fallback is a convenience, not a promise: a repository whose history
	// cannot be walked (a shallow CI clone, a broken describe) simply gets no
	// drafts rather than a failed release.
	last, err := repo.LastTag(comp.RenderTag("*"))
	if err != nil {
		return nil, "", "", nil
	}
	subjects, err := repo.Subjects(last)
	if err != nil {
		return nil, "", "", nil
	}
	drafted := changelog.FromCommits(subjects)
	if len(drafted) == 0 {
		return nil, "", "", nil
	}
	return drafted, FromCommits, last, nil
}

// writesChangelog reports whether this run renders a changelog file. An empty
// section means there is nothing to say, and an empty file means the component
// carries its entries into the tag only.
func (p *Plan) writesChangelog() bool {
	return p.Section != "" && p.Comp.Changelog.File != ""
}

// changelogCheck is the preflight check for what the release has to say about
// itself. It fails only under changelog.require: a release stamp cannot
// describe is still a correct release, and failing every repository over it
// would make the check the reason people stop reading the check list.
func (p *Plan) changelogCheck() {
	const label = "the release has changelog entries"
	cl := p.Comp.Changelog
	switch {
	case !p.Comp.ChangelogEnabled(p.Repo.Root):
		p.add(check{label: label, skipped: true,
			detail: "the changelog is not in use; `stamp note added \"…\"` starts it"})
	case len(p.Notes) == 0 && cl.Require:
		p.add(check{label: label, ok: false,
			detail: fmt.Sprintf("nothing noted; run `stamp note added \"…\"`, or set changelog.require to false in %s", config.FileName)})
	case len(p.Notes) == 0:
		p.add(check{label: label, skipped: true, detail: "nothing noted, the release section stays empty"})
	case p.NotesFrom == FromCommits:
		// A passing check prints its label and nothing else, so what the
		// entries are and where they came from has to be in the label.
		p.add(check{ok: true, label: fmt.Sprintf("the release has %s drafted from commits since %s",
			plural(len(p.Notes)), orElse(p.notesSince, "the first commit"))})
	default:
		p.add(check{ok: true, label: fmt.Sprintf("the release has %s from %s",
			plural(len(p.Notes)), cl.Dir)})
	}
}

func plural(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

func orElse(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// preflight fills p.checks. It returns an error only for problems that make the
// plan itself unresolvable; a failed check is reported through p.checks so the
// user sees all of them at once instead of fixing them one per run.
func (p *Plan) preflight() error {
	// The version relationship. An equal version is allowed and switches the
	// run into tag-only mode.
	equal, err := version.Compare(p.Current, p.Next)
	if err != nil {
		p.add(check{label: err.Error(), ok: false})
	} else {
		p.Unchanged = equal
		label := fmt.Sprintf("%s is newer than %s", p.Next, p.Current)
		switch {
		case equal && p.writesChangelog():
			// The version files need no write, but the changelog does, and
			// that write still has to be committed.
			label = fmt.Sprintf("version already at %s, committing the changelog and tagging HEAD", p.Next)
		case equal:
			label = fmt.Sprintf("version already at %s, tagging HEAD, no commit", p.Next)
		}
		p.add(check{label: label, ok: true})
	}

	// Every further location must currently agree with the first one. A file
	// that has drifted is a sign something bumped one place by hand, and
	// silently overwriting it would hide that.
	for _, m := range p.Comp.Mirrors() {
		label := fmt.Sprintf("%s agrees with %s", m.Describe(), p.Comp.Source().Path())
		got, err := m.Read()
		switch {
		case err != nil:
			p.add(check{label: fmt.Sprintf("%s is readable", m.Describe()), ok: false, detail: err.Error()})
		case got != p.Current && got != p.Next:
			p.add(check{label: label, ok: false, detail: fmt.Sprintf("holds %s, expected %s", got, p.Current)})
		default:
			p.add(check{label: label, ok: true})
		}
	}

	if !p.Repo.HasCommits() {
		p.add(check{label: "repository has at least one commit", ok: false,
			detail: "the repository is empty, commit something before releasing"})
		return nil
	}

	branch, err := p.Repo.CurrentBranch()
	if err != nil {
		p.add(check{label: "on a branch", ok: false, detail: err.Error()})
		return nil
	}
	p.add(check{
		label:  fmt.Sprintf("on branch %s", p.Branch),
		ok:     branch == p.Branch,
		detail: fmt.Sprintf("HEAD is on %s, check out %s, set release.branch in %s, or pass --branch %s", branch, p.Branch, config.FileName, branch),
	})

	clean, err := p.Repo.IsClean()
	if err != nil {
		return err
	}
	dirtyDetail := ""
	if !clean {
		paths, _ := p.Repo.DirtyPaths()
		dirtyDetail = fmt.Sprintf("%d uncommitted change(s); the release commit must hold only the version bump", len(paths))
	}
	p.add(check{label: "working tree clean", ok: clean, detail: dirtyDetail})

	// Remote-facing checks. Without --no-fetch these need the network; a
	// failure to reach the remote is reported as a failed check rather than
	// swallowed, because "could not verify" is not the same as "fine".
	if p.Options.NoFetch {
		p.add(check{label: "branch up to date with the remote", skipped: true, detail: "skipped (--no-fetch)"})
	} else if err := p.Repo.Fetch(p.Remote); err != nil {
		p.add(check{label: fmt.Sprintf("fetch %s", p.Remote), ok: false, detail: err.Error()})
	} else {
		p.upstreamCheck(branch)
	}

	exists, err := p.Repo.TagExists(p.Tag)
	if err != nil {
		return err
	}
	p.add(check{label: fmt.Sprintf("tag %s does not exist", p.Tag), ok: !exists,
		detail: fmt.Sprintf("%s already exists locally", p.Tag)})

	if !p.Options.NoFetch && !exists {
		// The fetch above brought the remote's tags in, so a remote-only tag
		// would already have shown up locally. This is the belt-and-braces
		// check for a remote that rejected the tag fetch.
		if remote, err := p.Repo.RemoteTagExists(p.Remote, p.Tag); err == nil && remote {
			p.add(check{label: fmt.Sprintf("tag %s free on %s", p.Tag, p.Remote), ok: false,
				detail: fmt.Sprintf("%s already exists on %s", p.Tag, p.Remote)})
		}
	}

	p.changelogCheck()
	return nil
}

func (p *Plan) upstreamCheck(branch string) {
	upstream, ok, err := p.Repo.Upstream(branch)
	if err != nil {
		p.add(check{label: "branch up to date with the remote", ok: false, detail: err.Error()})
		return
	}
	if !ok {
		// No upstream yet: the first push will create it. Not a failure, but
		// worth saying out loud, because the push will need -u semantics.
		p.add(check{label: "branch up to date with the remote", skipped: true,
			detail: fmt.Sprintf("%s has no upstream yet, the push will create it", branch)})
		return
	}
	ahead, behind, err := p.Repo.Divergence(branch, upstream)
	if err != nil {
		p.add(check{label: fmt.Sprintf("up to date with %s", upstream), ok: false, detail: err.Error()})
		return
	}
	if behind > 0 {
		p.add(check{label: fmt.Sprintf("up to date with %s", upstream), ok: false,
			detail: fmt.Sprintf("%d commit(s) behind, pull first", behind)})
		return
	}
	label := fmt.Sprintf("up to date with %s", upstream)
	if ahead > 0 {
		label = fmt.Sprintf("%d commit(s) ahead of %s, nothing behind", ahead, upstream)
	}
	p.add(check{label: label, ok: true})
}

func (p *Plan) add(c check) { p.checks = append(p.checks, c) }

// OK reports whether every check passed.
func (p *Plan) OK() bool {
	for _, c := range p.checks {
		if !c.ok && !c.skipped {
			return false
		}
	}
	return true
}

// WillPush reports whether this run ends in a push.
func (p *Plan) WillPush() bool { return p.Comp.Push && !p.Options.NoPush }

// Print renders the plan and the check list.
func (p *Plan) Print() {
	title := "Release %s"
	if p.Options.Pre {
		title = "Pre-release %s"
	}
	ui.Title(title, p.Comp.Label())
	if p.Config.Multi {
		ui.Field("Component", fmt.Sprintf("%s of %s", p.Comp.Name, p.Config.ProjectName))
	}
	ui.Field("Version", ui.Bump(p.Current, p.Next))
	if pre := version.PreOf(p.Next); pre != "" {
		ui.Field("Pre-release", fmt.Sprintf("%s (not a stable release)", pre))
	}
	if !p.Unchanged || p.writesChangelog() {
		ui.Field("Commit", p.Commit)
	}
	ui.Field("Tag", p.Tag)
	ui.Field("Branch", p.Branch)
	if p.WillPush() {
		ui.Field("Remote", p.Remote)
	} else {
		ui.Field("Remote", "(not pushing)")
	}
	if !p.Config.FromFile {
		ui.Field("Config", fmt.Sprintf("none, detected %s", p.Comp.Source().Describe()))
	}

	if !p.Unchanged || p.writesChangelog() {
		ui.Section("Files to update:")
		if !p.Unchanged {
			for _, s := range p.Comp.Sources {
				ui.Item(s.Describe())
			}
		}
		if p.writesChangelog() {
			ui.Item(p.Comp.Changelog.File)
		}
	}

	p.printNotes()

	ui.Section("Checks:")
	for _, c := range p.checks {
		switch {
		case c.skipped:
			ui.Skip(fmt.Sprintf("%s: %s", c.label, c.detail))
		case c.ok:
			ui.Pass(c.label)
		case c.detail != "":
			ui.Fail(fmt.Sprintf("%s: %s", c.label, c.detail))
		default:
			ui.Fail(c.label)
		}
	}
}

// maxListedNotes caps the entries printed in the plan. A release with fifty
// notes is a real thing, and printing all of them would push the check list,
// which is what the user is here to read, off the top of the screen.
const maxListedNotes = 10

// printNotes shows what the release will publish. Drafted entries are labelled
// as drafts every time they are shown: a user must never mistake a line stamp
// wrote from a commit subject for a line a human wrote for them.
func (p *Plan) printNotes() {
	if len(p.Notes) == 0 {
		return
	}
	ui.Section("Changelog:")
	if p.NotesFrom == FromCommits {
		ui.Note("Drafted from the commits since %s, not noted by hand. Review them, or edit with --edit.",
			orElse(p.notesSince, "the first commit"))
	}
	for i, e := range p.Notes {
		if i == maxListedNotes {
			ui.Item(fmt.Sprintf("… and %d more", len(p.Notes)-maxListedNotes))
			break
		}
		ui.Item(fmt.Sprintf("%s: %s", e.Kind.Heading(), firstLine(e.Text)))
	}
	if !p.writesChangelog() && p.Comp.Changelog.TagBody {
		ui.Note("No changelog file is configured; the entries go into the tag only.")
	}
}

// firstLine keeps a multi-paragraph entry to one line in the plan; the whole of
// it still goes into the changelog.
func firstLine(text string) string {
	line, rest, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if strings.TrimSpace(rest) != "" {
		return line + " …"
	}
	return line
}

// snapshot is a file's contents before stamp wrote to it, kept in memory so a
// failure can put it back.
type snapshot struct {
	path string // repository-relative
	data []byte
	mode os.FileMode
	// created is true when stamp created the file, so putting it back means
	// removing it again. Writing an empty file in its place would leave a
	// CHANGELOG.md behind that nobody asked for, and, worse, would switch the
	// changelog on for every later release of a repository that had opted out.
	created bool
	// desc says what the restore amounted to, for the "Restored:" list.
	desc string
}

// Run executes the plan. It assumes Print and the confirmation already happened.
func (p *Plan) Run() error {
	var written []snapshot

	// restore puts every written file back and reports what it restored. It is
	// passed to fail, which prints it *after* the reason for the abort, because the
	// user wants to read why it stopped before what it undid.
	restore := func() {
		if len(written) == 0 {
			return
		}
		ui.Step("Restored:")
		for _, s := range written {
			abs := filepath.Join(p.Repo.Root, s.path)
			if s.created {
				if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
					ui.Errorf("could not remove %s: %v", s.path, err)
					continue
				}
				ui.Item(fmt.Sprintf("%s → removed", s.path))
				continue
			}
			if err := os.WriteFile(abs, s.data, s.mode); err != nil {
				ui.Errorf("could not restore %s: %v", s.path, err)
				continue
			}
			ui.Item(fmt.Sprintf("%s → %s", s.path, s.desc))
		}
	}

	if !p.Unchanged {
		for _, src := range p.Comp.Sources {
			abs := filepath.Join(p.Repo.Root, src.Path())
			data, mode, err := readWithMode(abs)
			if err != nil {
				return fail(err, restore, notNothing...)
			}
			if err := src.Write(p.Next); err != nil {
				return fail(fmt.Errorf("writing %s: %w", src.Path(), err), restore, notNothing...)
			}
			written = append(written, snapshot{path: src.Path(), data: data, mode: mode, desc: p.Current})
			ui.Step("  updated %s", src.Describe())
		}
	}

	// The changelog, after the version files and before the commit, so a
	// failure anywhere in here is undone by the same restore closure and the
	// release stops with nothing committed.
	changelogPaths, err := p.writeChangelog(&written)
	if err != nil {
		return fail(err, restore, notNothing...)
	}

	// Commit. The version files are staged explicitly, never `commit -a`,
	// so nothing that appeared in the tree meanwhile can slip into the release
	// commit.
	//
	// The version bump and the changelog go in together: one commit is the
	// whole release. A first release, whose version files already hold the
	// target version, still commits when it has a changelog to publish.
	var paths []string
	if !p.Unchanged {
		paths = append(paths, p.Comp.Paths()...)
	}
	paths = append(paths, changelogPaths...)

	committed := false
	if len(paths) > 0 {
		changed, err := p.Repo.HasChanges(paths...)
		if err != nil {
			return fail(err, restore, notNothing...)
		}
		if changed {
			if err := p.Repo.Add(paths...); err != nil {
				return fail(err, restore, notNothing...)
			}
			if err := p.Repo.Commit(p.Commit); err != nil {
				// Unstage before restoring, so the index does not keep a
				// staged version bump that the working tree no longer has.
				if uerr := p.Repo.Unstage(paths...); uerr != nil {
					ui.Errorf("could not unstage: %v", uerr)
				}
				return fail(err, restore, notNothing...)
			}
			committed = true
			ui.Step("  committed %q", p.Commit)
		}
	}

	// Tag. A failure here leaves the commit in place: it is a valid commit and
	// throwing it away would mean rewriting history the user may already be
	// looking at. Retrying is a single tag command, which we print.
	if err := p.Repo.Tag(p.Tag, p.tagMessage()); err != nil {
		ui.Blank()
		ui.Errorf("creating tag %s failed: %v", p.Tag, err)
		if committed {
			ui.Hint("the release commit %q was created and was kept; it is valid on its own", p.Commit)
			ui.Hint("continue with: git tag -a %s -m %s && git push %s %s %s", p.Tag, p.Tag, p.Remote, p.Branch, p.Tag)
		}
		return errAborted
	}
	ui.Step("  tagged %s", p.Tag)

	if !p.WillPush() {
		ui.Blank()
		reason := "--no-push"
		if !p.Comp.Push {
			reason = "release.push is false"
		}
		ui.Note("Not pushing (%s).", reason)
		ui.Note("When ready: git push %s %s %s", p.Remote, p.Branch, p.Tag)
		return nil
	}

	// Branch and tag go out in one push, so the remote can never end up with a
	// tag whose commit it does not have.
	ui.Blank()
	ui.Step("Pushing %s and %s to %s ...", p.Branch, p.Tag, p.Remote)
	if err := p.Repo.Push(p.Remote, p.Branch, p.Tag); err != nil {
		ui.Blank()
		ui.Errorf("push failed: %v", err)
		ui.Hint("nothing was rolled back; the commit and the tag exist locally and are valid")
		ui.Hint("retry with: git push %s %s %s", p.Remote, p.Branch, p.Tag)
		ui.Hint("or undo locally with: git tag -d %s%s", p.Tag, undoCommitHint(committed))
		return errAborted
	}
	return nil
}

// writeChangelog renders the section into the changelog file and removes the
// fragments it came from, appending a snapshot of everything it touched to
// written so the caller's restore closure can put it all back.
//
// It returns the repository-relative paths that have to be staged. A deleted
// fragment is one of them: `git add` on a path that is gone records the
// deletion, which is what puts the removal in the release commit.
//
// Entries drafted from commits carry no file, so nothing is deleted for them.
// Directories are left in place even when they end up empty, which is what
// keeps restoring a plain write.
func (p *Plan) writeChangelog(written *[]snapshot) ([]string, error) {
	if !p.writesChangelog() {
		return nil, nil
	}
	rel := filepath.FromSlash(p.Comp.Changelog.File)
	abs := filepath.Join(p.Repo.Root, rel)

	data, mode, err := readWithMode(abs)
	created := false
	if os.IsNotExist(err) {
		// A missing changelog is the ordinary first time: Insert writes the
		// file header and the section into it.
		data, mode, created, err = nil, changelogMode, true, nil
	}
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), dirMode); err != nil {
		return nil, err
	}
	if err := os.WriteFile(abs, changelog.Insert(data, p.Section), mode); err != nil {
		return nil, fmt.Errorf("writing %s: %w", p.Comp.Changelog.File, err)
	}
	*written = append(*written, snapshot{path: rel, data: data, mode: mode, created: created, desc: "restored"})
	ui.Step("  updated %s", p.Comp.Changelog.File)

	paths := []string{p.Comp.Changelog.File}
	removed := 0
	for _, e := range p.Notes {
		if e.File == "" {
			continue
		}
		frag := filepath.FromSlash(e.File)
		fabs := filepath.Join(p.Repo.Root, frag)
		fdata, fmode, err := readWithMode(fabs)
		if err != nil {
			return paths, err
		}
		if err := os.Remove(fabs); err != nil {
			return paths, fmt.Errorf("removing %s: %w", e.File, err)
		}
		*written = append(*written, snapshot{path: frag, data: fdata, mode: fmode, desc: "restored"})
		paths = append(paths, e.File)
		removed++
	}
	if removed > 0 {
		ui.Step("  collected %s from %s", plural(removed), p.Comp.Changelog.Dir)
	}
	return paths, nil
}

// tagMessage is the annotated tag's message: the tag name, and under it the
// release notes, so the pipeline can read them straight off the tag instead of
// generating something else.
func (p *Plan) tagMessage() string {
	if !p.Comp.Changelog.TagBody || p.Section == "" {
		return p.Tag
	}
	body := changelog.Body(p.Section)
	if body == "" {
		return p.Tag
	}
	return p.Tag + "\n\n" + body
}

// Modes for what the changelog stage creates. A changelog is an ordinary source
// file that gets committed, so it takes the ordinary modes.
const (
	dirMode       = 0o755
	changelogMode = 0o644
)

func undoCommitHint(committed bool) string {
	if committed {
		return " && git reset --hard HEAD~1"
	}
	return ""
}

// errAborted marks a failure that has already been reported in full, so main
// exits non-zero without printing a second message.
var errAborted = errors.New("release aborted")

// IsAborted reports whether err was already reported to the user.
func IsAborted(err error) bool { return errors.Is(err, errAborted) }

// notNothing is the reassurance printed under an abort: the three things that
// definitely did not happen.
var notNothing = []string{"No commit created.", "No tag created.", "Nothing pushed."}

// fail reports err, then what was rolled back, then what did *not* happen, and
// returns errAborted.
func fail(err error, restore func(), notes ...string) error {
	ui.Blank()
	ui.Errorf("%v", err)
	ui.Blank()
	ui.Step("Release aborted.")
	if restore != nil {
		restore()
	}
	for _, n := range notes {
		ui.Note("%s", n)
	}
	return errAborted
}

func readWithMode(path string) ([]byte, os.FileMode, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	return data, st.Mode().Perm(), nil
}
