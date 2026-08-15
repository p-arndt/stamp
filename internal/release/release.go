// Package release carries out a release: preflight, write, commit, tag, push.
//
// The order is deliberate — every check that can be made runs *before* the
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
}

// Plan is everything stamp resolved before touching the repository.
type Plan struct {
	Config  *config.Config
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
	// nothing to commit, so stamp tags the current HEAD.
	Unchanged bool

	checks []check
}

type check struct {
	label   string
	ok      bool
	skipped bool
	detail  string
}

// Prepare resolves the plan and runs every preflight check. It returns a plan
// even when checks failed, so the caller can print the full list.
func Prepare(cfg *config.Config, repo *gitx.Repo, opts Options) (*Plan, error) {
	p := &Plan{Config: cfg, Repo: repo, Options: opts, Remote: cfg.Remote}

	current, err := cfg.Source.Read()
	if err != nil {
		return nil, fmt.Errorf("reading the current version: %w", err)
	}
	p.Current = current

	next, err := resolve(cfg, opts, current)
	if err != nil {
		return nil, err
	}
	p.Next = next
	p.Tag = cfg.RenderTag(next)
	p.Commit = cfg.RenderCommit(next, p.Tag)

	wantBranch := cfg.Branch
	if opts.Branch != "" {
		wantBranch = opts.Branch
	}
	p.Branch = wantBranch

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
// is what keeps a series walkable — after `--type rc` put the project on
// 1.3.0-rc.1, a plain `stamp prerelease patch` has to continue at rc.2. Taking
// the configured default there would resolve to 1.3.0-beta.1, which is *lower*
// in semver, so the run would die in preflight and every further cut would need
// --type rc forever.
func resolve(cfg *config.Config, opts Options, current string) (string, error) {
	if !opts.Pre {
		return version.Resolve(current, opts.Arg)
	}
	id := opts.PreID
	if id == "" && version.ContinuesSeries(current, opts.Arg) {
		id = version.PreIDOf(current)
	}
	if id == "" {
		id = cfg.PreID
	}
	return version.ResolvePre(current, opts.Arg, id)
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
		if equal {
			label = fmt.Sprintf("version already at %s — tagging HEAD, no commit", p.Next)
		}
		p.add(check{label: label, ok: true})
	}

	// Every mirror must currently agree with the source. A mirror that has
	// drifted is a sign something bumped one place by hand, and silently
	// overwriting it would hide that.
	for _, m := range p.Config.Mirrors {
		got, err := m.Read()
		switch {
		case err != nil:
			p.add(check{label: fmt.Sprintf("mirror %s is readable", m.Describe()), ok: false, detail: err.Error()})
		case got != p.Current && got != p.Next:
			p.add(check{label: fmt.Sprintf("mirror %s agrees with %s", m.Describe(), p.Config.Source.Path()), ok: false,
				detail: fmt.Sprintf("holds %s, expected %s", got, p.Current)})
		default:
			p.add(check{label: fmt.Sprintf("mirror %s agrees with %s", m.Describe(), p.Config.Source.Path()), ok: true})
		}
	}

	if !p.Repo.HasCommits() {
		p.add(check{label: "repository has at least one commit", ok: false,
			detail: "the repository is empty — commit something before releasing"})
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
		detail: fmt.Sprintf("HEAD is on %s — check out %s, set release.branch in %s, or pass --branch %s", branch, p.Branch, config.FileName, branch),
	})

	clean, err := p.Repo.IsClean()
	if err != nil {
		return err
	}
	dirtyDetail := ""
	if !clean {
		paths, _ := p.Repo.DirtyPaths()
		dirtyDetail = fmt.Sprintf("%d uncommitted change(s) — the release commit must hold only the version bump", len(paths))
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
			detail: fmt.Sprintf("%s has no upstream yet — the push will create it", branch)})
		return
	}
	ahead, behind, err := p.Repo.Divergence(branch, upstream)
	if err != nil {
		p.add(check{label: fmt.Sprintf("up to date with %s", upstream), ok: false, detail: err.Error()})
		return
	}
	if behind > 0 {
		p.add(check{label: fmt.Sprintf("up to date with %s", upstream), ok: false,
			detail: fmt.Sprintf("%d commit(s) behind — pull first", behind)})
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
func (p *Plan) WillPush() bool { return p.Config.Push && !p.Options.NoPush }

// Print renders the plan and the check list.
func (p *Plan) Print() {
	title := "Release %s"
	if p.Options.Pre {
		title = "Pre-release %s"
	}
	ui.Title(title, p.Config.ProjectName)
	ui.Field("Version", ui.Bump(p.Current, p.Next))
	if pre := version.PreOf(p.Next); pre != "" {
		ui.Field("Pre-release", fmt.Sprintf("%s (not a stable release)", pre))
	}
	if !p.Unchanged {
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
		ui.Field("Config", fmt.Sprintf("none — detected %s", p.Config.Source.Describe()))
	}

	if !p.Unchanged {
		ui.Section("Files to update:")
		for _, s := range p.Config.AllSources() {
			ui.Item(s.Describe())
		}
	}

	ui.Section("Checks:")
	for _, c := range p.checks {
		switch {
		case c.skipped:
			ui.Skip(fmt.Sprintf("%s — %s", c.label, c.detail))
		case c.ok:
			ui.Pass(c.label)
		case c.detail != "":
			ui.Fail(fmt.Sprintf("%s — %s", c.label, c.detail))
		default:
			ui.Fail(c.label)
		}
	}
}

// snapshot is a file's contents before stamp wrote to it, kept in memory so a
// failure can put it back.
type snapshot struct {
	path string // repository-relative
	data []byte
	mode os.FileMode
}

// Run executes the plan. It assumes Print and the confirmation already happened.
func (p *Plan) Run() error {
	var written []snapshot

	// restore puts every written file back and reports what it restored. It is
	// passed to fail, which prints it *after* the reason for the abort — the
	// user wants to read why it stopped before what it undid.
	restore := func() {
		if len(written) == 0 {
			return
		}
		ui.Step("Restored:")
		for _, s := range written {
			if err := os.WriteFile(filepath.Join(p.Repo.Root, s.path), s.data, s.mode); err != nil {
				ui.Errorf("could not restore %s: %v", s.path, err)
				continue
			}
			ui.Item(fmt.Sprintf("%s → %s", s.path, p.Current))
		}
	}

	if !p.Unchanged {
		for _, src := range p.Config.AllSources() {
			abs := filepath.Join(p.Repo.Root, src.Path())
			data, mode, err := readWithMode(abs)
			if err != nil {
				return fail(err, restore, notNothing...)
			}
			if err := src.Write(p.Next); err != nil {
				return fail(fmt.Errorf("writing %s: %w", src.Path(), err), restore, notNothing...)
			}
			written = append(written, snapshot{path: src.Path(), data: data, mode: mode})
			ui.Step("  updated %s", src.Describe())
		}
	}

	// Commit. The version files are staged explicitly — never `commit -a` —
	// so nothing that appeared in the tree meanwhile can slip into the release
	// commit.
	committed := false
	if !p.Unchanged {
		changed, err := p.Repo.HasChanges(p.Config.Paths()...)
		if err != nil {
			return fail(err, restore, notNothing...)
		}
		if changed {
			if err := p.Repo.Add(p.Config.Paths()...); err != nil {
				return fail(err, restore, notNothing...)
			}
			if err := p.Repo.Commit(p.Commit); err != nil {
				// Unstage before restoring, so the index does not keep a
				// staged version bump that the working tree no longer has.
				if uerr := p.Repo.Unstage(p.Config.Paths()...); uerr != nil {
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
	if err := p.Repo.Tag(p.Tag, p.Tag); err != nil {
		ui.Blank()
		ui.Errorf("creating tag %s failed: %v", p.Tag, err)
		if committed {
			ui.Hint("the release commit %q was created and was kept — it is valid on its own", p.Commit)
			ui.Hint("continue with: git tag -a %s -m %s && git push %s %s %s", p.Tag, p.Tag, p.Remote, p.Branch, p.Tag)
		}
		return errAborted
	}
	ui.Step("  tagged %s", p.Tag)

	if !p.WillPush() {
		ui.Blank()
		reason := "--no-push"
		if !p.Config.Push {
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
		ui.Hint("nothing was rolled back — the commit and the tag exist locally and are valid")
		ui.Hint("retry with: git push %s %s %s", p.Remote, p.Branch, p.Tag)
		ui.Hint("or undo locally with: git tag -d %s%s", p.Tag, undoCommitHint(committed))
		return errAborted
	}
	return nil
}

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
