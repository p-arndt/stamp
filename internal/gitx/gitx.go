// Package gitx wraps the git commands stamp needs.
//
// It shells out to the git binary rather than using a library: stamp only needs
// a dozen plumbing calls, and going through the real git means it picks up the
// user's config, hooks, credential helpers and SSH agent exactly as an
// interactive git invocation would.
package gitx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Repo is a git repository on disk.
type Repo struct {
	Root string
}

// Open finds the repository containing dir.
func Open(dir string) (*Repo, error) {
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not inside a git repository")
	}
	return &Repo{Root: out}, nil
}

// CurrentBranch returns the checked-out branch name, or an error when HEAD is
// detached, because releasing from a detached HEAD would tag a commit that no branch
// points at.
func (r *Repo) CurrentBranch() (string, error) {
	out, err := r.git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "HEAD" {
		return "", errors.New("HEAD is detached, check out a branch first")
	}
	return out, nil
}

// IsClean reports whether the working tree and index have no changes at all,
// including untracked files. Untracked files count: they are usually a
// half-finished change that belongs in the release, and excluding them would
// make "clean" mean something subtly different from what `git status` shows.
func (r *Repo) IsClean() (bool, error) {
	out, err := r.git("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// DirtyPaths returns the porcelain status lines, for reporting which files are
// in the way.
func (r *Repo) DirtyPaths() ([]string, error) {
	out, err := r.git("status", "--porcelain")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// HasChanges reports whether any of paths differ from HEAD.
func (r *Repo) HasChanges(paths ...string) (bool, error) {
	args := append([]string{"status", "--porcelain", "--"}, paths...)
	out, err := r.git(args...)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// TagExists reports whether tag exists locally.
func (r *Repo) TagExists(tag string) (bool, error) {
	// --verify with a full refname avoids matching a branch of the same name.
	_, err := r.git("rev-parse", "-q", "--verify", "refs/tags/"+tag)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return false, nil // "not a valid ref", so the tag is free.
	}
	return false, err
}

// RemoteTagExists reports whether tag exists on the remote. This needs the
// network; a missing remote or an unreachable one is reported as an error so
// the caller can decide whether to treat it as fatal.
func (r *Repo) RemoteTagExists(remote, tag string) (bool, error) {
	out, err := r.git("ls-remote", "--tags", remote, "refs/tags/"+tag)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// Fetch updates the remote-tracking refs for remote.
func (r *Repo) Fetch(remote string) error {
	_, err := r.git("fetch", remote, "--tags")
	return err
}

// Upstream returns the configured upstream ref of branch (e.g.
// "origin/main"), or ok=false when the branch has none, which is the normal
// state of a brand-new repository before its first push.
func (r *Repo) Upstream(branch string) (ref string, ok bool, err error) {
	out, err := r.git("rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", false, nil
		}
		return "", false, err
	}
	return out, true, nil
}

// Divergence counts how many commits branch is ahead of and behind upstream.
func (r *Repo) Divergence(branch, upstream string) (ahead, behind int, err error) {
	out, err := r.git("rev-list", "--left-right", "--count", branch+"..."+upstream)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	if ahead, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, err
	}
	if behind, err = strconv.Atoi(fields[1]); err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

// Add stages paths.
func (r *Repo) Add(paths ...string) error {
	_, err := r.git(append([]string{"add", "--"}, paths...)...)
	return err
}

// Unstage removes paths from the index without touching the working tree.
func (r *Repo) Unstage(paths ...string) error {
	_, err := r.git(append([]string{"reset", "-q", "HEAD", "--"}, paths...)...)
	return err
}

// Commit creates a commit with message. Only what is already staged goes in:
// stamp stages the version files explicitly and never uses -a.
func (r *Repo) Commit(message string) error {
	_, err := r.git("commit", "-m", message)
	return err
}

// Tag creates an annotated tag on HEAD.
//
// --cleanup=verbatim because the message may carry a rendered changelog
// section, and git's default cleanup strips every line that starts with "#",
// which would silently swallow the "### Added" headings a pipeline reads the
// release notes out of.
func (r *Repo) Tag(tag, message string) error {
	_, err := r.git("tag", "-a", "--cleanup=verbatim", tag, "-m", message)
	return err
}

// DeleteTag removes a local tag, for rollback.
func (r *Repo) DeleteTag(tag string) error {
	_, err := r.git("tag", "-d", tag)
	return err
}

// Push sends branch and tag to remote in a single invocation, so the tag can
// never arrive without the commit it points at.
func (r *Repo) Push(remote, branch, tag string) error {
	args := []string{"push", remote, branch}
	if tag != "" {
		args = append(args, "refs/tags/"+tag)
	}
	return r.stream(args...)
}

// DefaultRemote returns "origin" when it exists, otherwise the only remote if
// there is exactly one.
func (r *Repo) DefaultRemote() (string, error) {
	out, err := r.git("remote")
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", errors.New("the repository has no remote configured")
	}
	remotes := strings.Split(out, "\n")
	for _, rem := range remotes {
		if rem == "origin" {
			return "origin", nil
		}
	}
	if len(remotes) == 1 {
		return remotes[0], nil
	}
	return "", fmt.Errorf("several remotes (%s) and none named origin, set release.remote in .stamp.yml", strings.Join(remotes, ", "))
}

// ShortHEAD returns the abbreviated commit hash of HEAD.
func (r *Repo) ShortHEAD() (string, error) {
	return r.git("rev-parse", "--short", "HEAD")
}

// LastTag returns the most recent tag reachable from HEAD that matches pattern,
// or "" when the repository has none yet.
//
// describe rather than `tag --list` because reachability is the point: a tag
// cut on another branch is not part of this branch's history, and the commits
// "since the last release" have to be the commits since the last release *of
// this line of development*. --abbrev=0 asks describe for the bare tag name
// instead of the tag+distance+hash form, and --match narrows it to this
// component's tags, so `web-v*` does not pick up `cli-v1.2.0`.
//
// describe exits non-zero with "no names found" when nothing matches, which is
// the ordinary state of a repository before its first release and so is
// reported as the empty string rather than as an error.
func (r *Repo) LastTag(pattern string) (string, error) {
	if !r.HasCommits() {
		return "", nil
	}
	args := []string{"describe", "--tags", "--abbrev=0"}
	if pattern != "" {
		args = append(args, "--match", pattern)
	}
	out, err := r.git(args...)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", nil // No tag matches; the whole history is "since".
		}
		return "", err
	}
	return out, nil
}

// Subjects returns the subject line of every commit after rev, oldest first. An
// empty rev means the whole history.
//
// --reverse puts the oldest first, which is the order a changelog reads in.
// --no-merges leaves out the merge commits, whose subjects describe the branch
// rather than the change. The range is "rev..HEAD", so the commit rev points at
// (the last release) is excluded, which is what "since the last tag" means.
func (r *Repo) Subjects(rev string) ([]string, error) {
	if !r.HasCommits() {
		return nil, nil
	}
	args := []string{"log", "--reverse", "--no-merges", "--format=%s"}
	if rev != "" {
		args = append(args, rev+"..HEAD")
	}
	out, err := r.git(args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// HasCommits reports whether HEAD resolves, i.e. the repository is not empty.
func (r *Repo) HasCommits() bool {
	_, err := r.git("rev-parse", "--verify", "HEAD")
	return err == nil
}

func (r *Repo) git(args ...string) (string, error) { return run(r.Root, args...) }

// stream runs git with its output attached to stamp's own, for commands whose
// progress the user wants to see (push).
func (r *Repo) stream(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Root
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %s (%w)", strings.Join(args, " "), msg, err)
	}
	return strings.TrimSpace(string(out)), nil
}
