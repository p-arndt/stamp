package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stampBin is the binary under test, built once for the whole package.
var stampBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stamp-test-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Windows will not execute a file without an executable extension, so the
	// test binary has to carry .exe there, because go build does not add it when -o
	// names the output explicitly.
	name := "stamp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	stampBin = filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", stampBin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building stamp:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// repo is a throwaway git repository with a bare remote, so pushes are real.
type repo struct {
	t      *testing.T
	dir    string
	remote string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	base := t.TempDir()
	r := &repo{t: t, dir: filepath.Join(base, "work"), remote: filepath.Join(base, "remote.git")}

	mustRun(t, base, "git", "init", "--bare", "-b", "main", r.remote)
	mustRun(t, base, "git", "init", "-b", "main", r.dir)
	mustRun(t, r.dir, "git", "config", "user.email", "test@example.com")
	mustRun(t, r.dir, "git", "config", "user.name", "Test")
	mustRun(t, r.dir, "git", "config", "commit.gpgsign", "false")
	mustRun(t, r.dir, "git", "config", "tag.gpgsign", "false")
	// Forward slashes even on Windows: a backslash path works as a git remote
	// most of the time, but git treats backslashes as escapes in some contexts,
	// and a file URL with forward slashes is unambiguous on every platform.
	mustRun(t, r.dir, "git", "remote", "add", "origin", filepath.ToSlash(r.remote))
	return r
}

// write creates or overwrites a file in the working tree.
func (r *repo) write(name, content string) {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) read(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// commitAll commits everything in the tree, giving the repo a clean state.
func (r *repo) commitAll(message string) {
	r.t.Helper()
	mustRun(r.t, r.dir, "git", "add", "-A")
	mustRun(r.t, r.dir, "git", "commit", "-q", "-m", message)
}

// push makes origin/main exist, so the up-to-date check has an upstream.
func (r *repo) push() {
	r.t.Helper()
	mustRun(r.t, r.dir, "git", "push", "-q", "-u", "origin", "main")
}

// seed sets up the common case: a VERSION file at 0.4.0, committed and pushed.
func (r *repo) seed(version string) {
	r.t.Helper()
	r.write("VERSION", version+"\n")
	r.write("README.md", "# test\n")
	r.commitAll("initial")
	r.push()
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	return mustRun(r.t, r.dir, "git", args...)
}

// remoteGit runs git inside the bare remote, to check what actually arrived.
func (r *repo) remoteGit(args ...string) string {
	r.t.Helper()
	return mustRun(r.t, r.remote, "git", args...)
}

// stamp runs the binary in the repository and returns its combined output plus
// the exit code.
func (r *repo) stamp(args ...string) (string, int) {
	r.t.Helper()
	cmd := exec.Command(stampBin, args...)
	cmd.Dir = r.dir
	// NO_COLOR keeps the assertions free of escape sequences.
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatalf("running stamp %v: %v", args, err)
	}
	return string(out), code
}

// mustStamp fails the test when stamp exits non-zero.
func (r *repo) mustStamp(args ...string) string {
	r.t.Helper()
	out, code := r.stamp(args...)
	if code != 0 {
		r.t.Fatalf("stamp %v exited %d:\n%s", args, code, out)
	}
	return out
}

func mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func requireContains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output does not contain %q:\n%s", w, out)
		}
	}
}

// ---------------------------------------------------------------------------
// release
// ---------------------------------------------------------------------------

func TestDryRunChangesNothing(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")
	head := r.git("rev-parse", "HEAD")

	out := r.mustStamp("release", "minor", "--dry-run")
	requireContains(t, out, "0.4.0", "0.5.0", "v0.5.0", "Dry run")

	if got := r.read("VERSION"); got != "0.4.0\n" {
		t.Errorf("VERSION = %q, a dry run must not write", got)
	}
	if r.git("rev-parse", "HEAD") != head {
		t.Error("a dry run created a commit")
	}
	if r.git("tag", "--list") != "" {
		t.Error("a dry run created a tag")
	}
}

func TestReleaseCommitsTagsAndPushes(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	out := r.mustStamp("release", "patch", "--yes")
	requireContains(t, out, "v0.4.1", "Done")

	if got := r.read("VERSION"); got != "0.4.1\n" {
		t.Errorf("VERSION = %q, want 0.4.1", got)
	}
	if msg := r.git("log", "-1", "--pretty=%s"); msg != "release: v0.4.1" {
		t.Errorf("commit message = %q", msg)
	}
	// The tag must be annotated, not lightweight.
	if kind := r.git("cat-file", "-t", "v0.4.1"); kind != "tag" {
		t.Errorf("tag object type = %q, want an annotated tag", kind)
	}
	// The release commit holds only the version file.
	if files := r.git("show", "--name-only", "--pretty=format:", "HEAD"); strings.TrimSpace(files) != "VERSION" {
		t.Errorf("release commit touched %q, want only VERSION", strings.TrimSpace(files))
	}
	// Branch and tag both arrived on the remote.
	if r.remoteGit("rev-parse", "refs/heads/main") != r.git("rev-parse", "HEAD") {
		t.Error("the branch did not reach the remote")
	}
	if !strings.Contains(r.remoteGit("tag", "--list"), "v0.4.1") {
		t.Error("the tag did not reach the remote")
	}
}

func TestReleaseWithoutYesRefusesWhenNotATerminal(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	out, code := r.stamp("release", "minor")
	if code == 0 {
		t.Fatalf("stamp should refuse to proceed unprompted:\n%s", out)
	}
	requireContains(t, out, "--yes")
	if r.git("tag", "--list") != "" {
		t.Error("a tag was created without confirmation")
	}
}

func TestReleaseNoPushKeepsEverythingLocal(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	out := r.mustStamp("release", "minor", "--yes", "--no-push")
	requireContains(t, out, "Not pushing", "git push origin main v0.5.0")

	if !strings.Contains(r.git("tag", "--list"), "v0.5.0") {
		t.Error("the tag should exist locally")
	}
	if strings.Contains(r.remoteGit("tag", "--list"), "v0.5.0") {
		t.Error("the tag reached the remote despite --no-push")
	}
	if r.remoteGit("rev-parse", "refs/heads/main") == r.git("rev-parse", "HEAD") {
		t.Error("the commit reached the remote despite --no-push")
	}
}

func TestReleaseExplicitAndPrereleaseVersions(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	r.mustStamp("release", "1.0.0-beta.1", "--yes")
	if got := r.read("VERSION"); got != "1.0.0-beta.1\n" {
		t.Fatalf("VERSION = %q", got)
	}
	if !strings.Contains(r.git("tag", "--list"), "v1.0.0-beta.1") {
		t.Error("the pre-release tag is missing")
	}

	// A pre-release can be promoted to the real release afterwards.
	r.mustStamp("release", "1.0.0", "--yes")
	if got := r.read("VERSION"); got != "1.0.0\n" {
		t.Errorf("VERSION = %q", got)
	}
}

// The full pre-release cycle: open a series, move within it, switch identifier,
// then promote to the stable release.
func TestPrereleaseCycle(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	steps := []struct {
		args []string
		want string
	}{
		{[]string{"prerelease", "minor", "--yes"}, "0.5.0-beta.1"},
		{[]string{"prerelease", "--yes"}, "0.5.0-beta.2"},
		{[]string{"prerelease", "--type", "rc", "--yes"}, "0.5.0-rc.1"},
		{[]string{"prerelease", "--yes"}, "0.5.0-rc.2"},
		{[]string{"release", "final", "--yes"}, "0.5.0"},
	}
	for _, step := range steps {
		r.mustStamp(step.args...)
		if got := r.read("VERSION"); got != step.want+"\n" {
			t.Fatalf("after stamp %v: VERSION = %q, want %q", step.args, got, step.want)
		}
		if !strings.Contains(r.git("tag", "--list"), "v"+step.want) {
			t.Fatalf("after stamp %v: tag v%s is missing", step.args, step.want)
		}
	}
	// Every tag reached the remote, in order.
	requireContains(t, r.remoteGit("tag", "--list"),
		"v0.5.0-beta.1", "v0.5.0-beta.2", "v0.5.0-rc.1", "v0.5.0")
}

// A bump keyword always bumps, off a pre-release as much as off a stable
// version, the same rule npm, uv and hatch follow. Which digits happen to be
// zero makes no difference, and the bare form is the repeatable one.
func TestPrereleaseBumpAlwaysBumps(t *testing.T) {
	cases := []struct{ from, bump, want string }{
		{"1.2.3", "minor", "1.3.0-beta.1"},
		{"1.3.0-beta.1", "patch", "1.3.1-beta.1"},
		{"1.3.0-beta.1", "minor", "1.4.0-beta.1"},
		{"1.3.0-beta.1", "major", "2.0.0-beta.1"},
		// The zero in the patch position used to change the answer. It no
		// longer does.
		{"1.3.1-beta.1", "minor", "1.4.0-beta.1"},
		{"1.3.0-rc.4", "minor", "1.4.0-beta.1"},
		// The bare form walks, so repeating it in a justfile is safe.
		{"1.3.0-beta.1", "", "1.3.0-beta.2"},
		{"1.3.1-beta.1", "", "1.3.1-beta.2"},
	}
	for _, c := range cases {
		r := newRepo(t)
		r.seed(c.from)
		args := []string{"prerelease", "--yes", "--no-fetch"}
		if c.bump != "" {
			args = append(args, c.bump)
		}
		r.mustStamp(args...)
		if got := strings.TrimSpace(r.read("VERSION")); got != c.want {
			t.Errorf("%s + %q = %s, want %s", c.from, c.bump, got, c.want)
		}
	}
}

// Once a series is running, --type is not needed again: the identifier is
// inherited from the current version. Taking the configured default instead
// would resolve backwards (rc.1 -> beta.1) and fail the preflight.
func TestPrereleaseStaysInItsSeries(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	r.mustStamp("prerelease", "minor", "--type", "rc", "--yes")
	// Bare: no bump needed to walk a running series.
	r.mustStamp("prerelease", "--yes")
	if got := r.read("VERSION"); got != "0.5.0-rc.2\n" {
		t.Fatalf("VERSION = %q, want 0.5.0-rc.2", got)
	}
	// A new series on a higher base does fall back to the default identifier.
	r.mustStamp("prerelease", "major", "--yes")
	if got := r.read("VERSION"); got != "1.0.0-beta.1\n" {
		t.Errorf("VERSION = %q, want 1.0.0-beta.1", got)
	}
}

// The identifier comes from the config when --type is absent, and the plan says
// out loud that this is not a stable release.
func TestPrereleaseIdentifierFromConfig(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write(".stamp.yml", "release:\n  prerelease: alpha\n")
	r.commitAll("initial")
	r.push()

	out := r.mustStamp("prerelease", "patch", "--dry-run")
	requireContains(t, out, "0.4.1-alpha.1", "Pre-release", "not a stable release")
	if got := r.read("VERSION"); got != "0.4.0\n" {
		t.Errorf("dry run wrote %q", got)
	}
}

// Without a running series there is nothing for a bare `stamp prerelease` to
// continue, and stamp says which keyword is missing rather than guessing one.
func TestBarePrereleaseNeedsASeries(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	out, code := r.stamp("prerelease", "--yes")
	if code == 0 {
		t.Fatalf("expected a failure, got:\n%s", out)
	}
	requireContains(t, out, "0.4.0 is not a pre-release", "patch, minor or major")
}

// An explicit version is `stamp release`'s job; prerelease only takes a bump.
func TestPrereleaseRejectsExplicitVersion(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	out, code := r.stamp("prerelease", "1.0.0-beta.1", "--yes")
	if code == 0 {
		t.Fatalf("expected a failure, got:\n%s", out)
	}
	requireContains(t, out, "stamp release")
}

// A package.json project: the version changes and nothing else does.
func TestReleaseKeepsPackageJSONFormatting(t *testing.T) {
	const pkg = "{\n\t\"name\": \"thing\",\n\t\"version\": \"0.1.0\",\n\t\"scripts\": {\n\t\t\"build\": \"vite build\"\n\t}\n}\n"

	r := newRepo(t)
	r.write("package.json", pkg)
	r.commitAll("initial")
	r.push()

	r.mustStamp("release", "minor", "--yes")

	want := strings.Replace(pkg, "0.1.0", "0.2.0", 1)
	if got := r.read("package.json"); got != want {
		t.Errorf("package.json =\n%q\nwant\n%q", got, want)
	}
	// One changed line in the diff, nothing else.
	diff := r.git("show", "--unified=0", "--pretty=format:", "HEAD")
	if added := strings.Count(diff, "\n+\t\""); added != 1 {
		t.Errorf("expected exactly one added line, got:\n%s", diff)
	}
}

func TestReleaseUpdatesMirrorsInOneCommit(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write("package.json", "{\n  \"version\": \"0.4.0\"\n}\n")
	r.write(".stamp.yml", `
project: mirrored
version:
  - VERSION
  - package.json#version
`)
	r.commitAll("initial")
	r.push()

	out := r.mustStamp("release", "minor", "--yes")
	requireContains(t, out, "Release mirrored", "package.json#version")

	if got := r.read("VERSION"); got != "0.5.0\n" {
		t.Errorf("VERSION = %q", got)
	}
	if got := r.read("package.json"); !strings.Contains(got, `"version": "0.5.0"`) {
		t.Errorf("package.json = %q", got)
	}
	files := strings.Fields(r.git("show", "--name-only", "--pretty=format:", "HEAD"))
	if len(files) != 2 {
		t.Errorf("release commit touched %v, want both version files in one commit", files)
	}
}

// A drifted mirror is a sign someone bumped one place by hand; stamp must stop
// rather than paper over it.
func TestReleaseRefusesDriftedMirror(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write("package.json", "{\n  \"version\": \"0.1.0\"\n}\n")
	r.write(".stamp.yml", "version:\n  - VERSION\n  - package.json#version\n")
	r.commitAll("initial")
	r.push()

	out, code := r.stamp("release", "minor", "--yes")
	if code == 0 {
		t.Fatalf("a drifted mirror should fail the preflight:\n%s", out)
	}
	requireContains(t, out, "holds 0.1.0", "preflight failed")
}

// Tag templates without the v prefix (uprox) must work end to end.
func TestReleaseWithBareTagTemplate(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.9.0\n")
	r.write(".stamp.yml", "release:\n  tag: \"{{ version }}\"\n  commit: \"chore: release {{ version }}\"\n")
	r.commitAll("initial")
	r.push()

	r.mustStamp("release", "minor", "--yes")
	if tags := r.git("tag", "--list"); tags != "0.10.0" {
		t.Errorf("tags = %q, want 0.10.0 without a prefix", tags)
	}
	if msg := r.git("log", "-1", "--pretty=%s"); msg != "chore: release 0.10.0" {
		t.Errorf("commit message = %q", msg)
	}
}

// The first-release case: the version file already holds the target version, so
// there is nothing to commit and stamp tags HEAD.
func TestReleaseSameVersionTagsHead(t *testing.T) {
	r := newRepo(t)
	r.seed("0.1.0")
	head := r.git("rev-parse", "HEAD")

	out := r.mustStamp("release", "0.1.0", "--yes")
	requireContains(t, out, "version already at 0.1.0")

	if r.git("rev-parse", "HEAD") != head {
		t.Error("a commit was created even though nothing changed")
	}
	if r.git("rev-list", "-n1", "v0.1.0") != head {
		t.Error("the tag does not point at HEAD")
	}
}

// ---------------------------------------------------------------------------
// preflight
// ---------------------------------------------------------------------------

func TestPreflightRejects(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(r *repo)
		args    []string
		wantOut string
	}{
		{
			name:    "dirty working tree",
			setup:   func(r *repo) { r.write("scratch.txt", "wip\n") },
			args:    []string{"release", "minor", "--yes"},
			wantOut: "working tree clean",
		},
		{
			name:    "wrong branch",
			setup:   func(r *repo) { mustRun(r.t, r.dir, "git", "checkout", "-q", "-b", "feature") },
			args:    []string{"release", "minor", "--yes"},
			wantOut: "on branch main",
		},
		{
			name:    "tag already exists",
			setup:   func(r *repo) { mustRun(r.t, r.dir, "git", "tag", "-a", "v0.5.0", "-m", "v0.5.0") },
			args:    []string{"release", "minor", "--yes"},
			wantOut: "tag v0.5.0 does not exist",
		},
		{
			name:    "version goes backwards",
			setup:   func(r *repo) {},
			args:    []string{"release", "0.1.0", "--yes"},
			wantOut: "lower than the current version",
		},
		{
			name: "branch behind the remote",
			setup: func(r *repo) {
				// Advance the remote behind our back, then drop the local
				// commit so main is strictly behind origin/main.
				mustRun(r.t, r.dir, "git", "commit", "-q", "--allow-empty", "-m", "remote work")
				r.push()
				mustRun(r.t, r.dir, "git", "reset", "-q", "--hard", "HEAD~1")
			},
			args:    []string{"release", "minor", "--yes"},
			wantOut: "behind",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			r.seed("0.4.0")
			head := r.git("rev-parse", "HEAD")
			c.setup(r)

			out, code := r.stamp(c.args...)
			if code == 0 {
				t.Fatalf("expected a failure:\n%s", out)
			}
			requireContains(t, out, c.wantOut)
			// Whatever the reason, nothing was written.
			if got := r.read("VERSION"); got != "0.4.0\n" {
				t.Errorf("VERSION = %q, want it untouched", got)
			}
			if r.git("rev-parse", "HEAD") != head {
				t.Error("a commit was created despite a failed preflight")
			}
		})
	}
}

func TestBranchFlagOverridesConfiguredBranch(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")
	mustRun(t, r.dir, "git", "checkout", "-q", "-b", "hotfix")
	mustRun(t, r.dir, "git", "push", "-q", "-u", "origin", "hotfix")

	out := r.mustStamp("release", "patch", "--yes", "--branch", "hotfix")
	requireContains(t, out, "on branch hotfix")
	if !strings.Contains(r.remoteGit("tag", "--list"), "v0.4.1") {
		t.Error("the tag did not reach the remote")
	}
}

func TestNoFetchSkipsTheRemoteChecks(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")
	// Point origin at nothing reachable: with --no-fetch the preflight must
	// still pass, and only the push at the end would fail.
	mustRun(t, r.dir, "git", "remote", "set-url", "origin", filepath.Join(r.dir, "does-not-exist.git"))

	out := r.mustStamp("release", "minor", "--yes", "--no-push", "--no-fetch")
	requireContains(t, out, "skipped (--no-fetch)", "v0.5.0")
}

// ---------------------------------------------------------------------------
// rollback
// ---------------------------------------------------------------------------

// When writing a mirror fails, the source that was already written has to go
// back, and no commit or tag may be left behind.
func TestRollbackRestoresWrittenFiles(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write("package.json", "{\n  \"version\": \"0.4.0\"\n}\n")
	r.write(".stamp.yml", "version:\n  - VERSION\n  - package.json#version\n")
	r.commitAll("initial")
	r.push()
	head := r.git("rev-parse", "HEAD")

	// Make the mirror unwritable, so it fails after VERSION has been written.
	if err := os.Chmod(filepath.Join(r.dir, "package.json"), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(r.dir, "package.json"), 0o644) })

	out, code := r.stamp("release", "minor", "--yes")
	if code == 0 {
		t.Fatalf("expected the release to fail:\n%s", out)
	}
	requireContains(t, out, "Release aborted", "Restored:", "VERSION → 0.4.0",
		"No commit created.", "No tag created.", "Nothing pushed.")

	if got := r.read("VERSION"); got != "0.4.0\n" {
		t.Errorf("VERSION = %q, want it restored to 0.4.0", got)
	}
	if r.git("rev-parse", "HEAD") != head {
		t.Error("a commit was left behind")
	}
	if r.git("tag", "--list") != "" {
		t.Error("a tag was left behind")
	}
	// The index must be clean too: no half-staged version bump.
	if staged := r.git("diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("index holds %q", staged)
	}
}

// ---------------------------------------------------------------------------
// current / set / verify
// ---------------------------------------------------------------------------

func TestCurrentPrintsBareVersion(t *testing.T) {
	r := newRepo(t)
	r.seed("1.2.3")

	out := r.mustStamp("current")
	if out != "1.2.3\n" {
		t.Errorf("output = %q, want a bare version for use in scripts", out)
	}
}

// init describes the repository as it already is, and the file it writes is
// immediately usable by the next command.
func TestInitWritesAUsableConfig(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")
	r.write("package.json", "{\n  \"version\": \"0.4.0\"\n}\n")
	r.commitAll("add package.json")

	out := r.mustStamp("init")
	requireContains(t, out, "Wrote .stamp.yml", "VERSION", "package.json#version", "v0.4.0")

	// The package.json beside a VERSION file becomes a mirror, so a release
	// bumps both.
	r.commitAll("add config")
	r.mustStamp("release", "minor", "--yes", "--no-fetch")
	if got := strings.TrimSpace(r.read("VERSION")); got != "0.5.0" {
		t.Errorf("VERSION = %q", got)
	}
	if !strings.Contains(r.read("package.json"), `"0.5.0"`) {
		t.Errorf("package.json was not mirrored:\n%s", r.read("package.json"))
	}
}

// A second init is refused rather than silently overwriting a hand-edited
// config; --dry-run and --force are the ways through.
func TestInitDoesNotOverwrite(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")
	r.mustStamp("init")
	before := r.read(".stamp.yml")

	out, code := r.stamp("init", "--name", "other")
	if code == 0 {
		t.Fatalf("a second init should fail:\n%s", out)
	}
	requireContains(t, out, "already exists")
	if r.read(".stamp.yml") != before {
		t.Error(".stamp.yml was overwritten by a failing init")
	}

	if out := r.mustStamp("init", "--dry-run", "--name", "other"); !strings.Contains(out, "project: other") {
		t.Errorf("--dry-run did not print the file:\n%s", out)
	}
	if r.read(".stamp.yml") != before {
		t.Error("--dry-run wrote the file")
	}

	r.mustStamp("init", "--force", "--name", "other")
	if !strings.Contains(r.read(".stamp.yml"), "project: other") {
		t.Error("--force did not overwrite")
	}
}

// The repository's own branch and remote beat the defaults: a repo on another
// branch must not be handed a config that says "main".
func TestInitTakesBranchAndRemoteFromTheRepository(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")
	r.git("checkout", "-q", "-b", "trunk")

	out := r.mustStamp("init")
	requireContains(t, out, "trunk", "origin")
	if !strings.Contains(r.read(".stamp.yml"), "branch: trunk") {
		t.Errorf("branch not taken from the repository:\n%s", r.read(".stamp.yml"))
	}
}

func TestSetWritesWithoutTouchingGit(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write("package.json", "{\n  \"version\": \"0.4.0\"\n}\n")
	r.write(".stamp.yml", "version:\n  - VERSION\n  - package.json#version\n")
	r.commitAll("initial")
	head := r.git("rev-parse", "HEAD")

	r.mustStamp("set", "minor")
	if got := r.read("VERSION"); got != "0.5.0\n" {
		t.Errorf("VERSION = %q", got)
	}
	if got := r.read("package.json"); !strings.Contains(got, `"version": "0.5.0"`) {
		t.Errorf("package.json = %q", got)
	}
	if r.git("rev-parse", "HEAD") != head {
		t.Error("set created a commit")
	}
	if r.git("tag", "--list") != "" {
		t.Error("set created a tag")
	}

	// set is the correction command, so it may also go backwards.
	r.mustStamp("set", "0.4.0")
	if got := r.read("VERSION"); got != "0.4.0\n" {
		t.Errorf("VERSION = %q, set should allow going back", got)
	}
}

func TestVerify(t *testing.T) {
	r := newRepo(t)
	r.seed("0.5.0")

	out := r.mustStamp("verify", "--tag", "v0.5.0")
	requireContains(t, out, "tag matches the committed version")

	out, code := r.stamp("verify", "--tag", "v0.6.0")
	if code == 0 {
		t.Fatalf("a mismatching tag must exit non-zero:\n%s", out)
	}
	requireContains(t, out, "does not match")

	// The positional form is accepted too, for terse CI steps.
	if out := r.mustStamp("verify", "v0.5.0"); !strings.Contains(out, "matches") {
		t.Errorf("positional tag form failed:\n%s", out)
	}
}

func TestVerifyChecksMirrors(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.5.0\n")
	r.write("package.json", "{\n  \"version\": \"0.4.0\"\n}\n")
	r.write(".stamp.yml", "version:\n  - VERSION\n  - package.json#version\n")
	r.commitAll("initial")

	out, code := r.stamp("verify", "--tag", "v0.5.0")
	if code == 0 {
		t.Fatalf("a lagging mirror must fail verification:\n%s", out)
	}
	requireContains(t, out, "holds 0.4.0")
}

// ---------------------------------------------------------------------------
// misc
// ---------------------------------------------------------------------------

func TestOutsideAGitRepository(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(stampBin, "current")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a failure outside a repository:\n%s", out)
	}
	requireContains(t, string(out), "not inside a git repository")
}

// The test binary is built without -ldflags, so it reports itself as "dev".
// Both update commands must refuse that before any network call: a source build
// has no release to compare against, and swapping a `go build` output for a
// release binary is never what the developer running it meant.
func TestUpdateCommandsRefuseDevBuild(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	for _, cmd := range []string{"check-update", "self-update"} {
		out, code := r.stamp(cmd)
		if code == 0 {
			t.Fatalf("stamp %s on a dev build should exit non-zero:\n%s", cmd, out)
		}
		requireContains(t, out, `"dev" build`)
	}
}

// The update commands operate on the installed binary, not on a project, so they
// must work with no repository in sight. The failure below is the dev-build
// refusal, which proves the command ran rather than being turned away by the
// repository lookup every other command starts with.
func TestUpdateCommandsDoNotNeedARepository(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(stampBin, "check-update")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the dev-build refusal:\n%s", out)
	}
	if strings.Contains(string(out), "not inside a git repository") {
		t.Errorf("check-update must not require a repository:\n%s", out)
	}
	requireContains(t, string(out), `"dev" build`)
}

// A dev build must also stay silent about updates: `stamp current` is meant for
// shell substitution, and a stray notice would be noise in every dev run.
func TestCurrentSilentAboutUpdatesOnDevBuild(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")

	out := r.mustStamp("current")
	if strings.TrimSpace(out) != "0.4.0" {
		t.Errorf("current printed more than the version: %q", out)
	}
}

// ---------------------------------------------------------------------------
// components
// ---------------------------------------------------------------------------

// monorepo is two components in one repository: cli at the root, web in a
// subdirectory, each with its own version and its own tag.
func monorepo(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write("web/package.json", "{\n  \"version\": \"1.0.0\"\n}\n")
	r.write(".stamp.yml", `
project: mono

release:
  branch: main
  remote: origin
  tag: "{{component}}-v{{version}}"

components:
  cli:
    version: VERSION
    tag: v{{version}}
  web:
    version: web/package.json#version
`)
	r.commitAll("initial")
	r.push()
	return r
}

// The whole point of components: releasing one leaves the other alone.
func TestComponentReleaseTouchesOnlyItsOwnFiles(t *testing.T) {
	r := monorepo(t)

	out := r.mustStamp("release", "web", "minor", "--yes")
	requireContains(t, out, "Release web", "web-v1.1.0")

	if got := strings.TrimSpace(r.read("VERSION")); got != "0.4.0" {
		t.Errorf("VERSION = %q, the cli component was bumped too", got)
	}
	if !strings.Contains(r.read("web/package.json"), `"1.1.0"`) {
		t.Errorf("web was not bumped:\n%s", r.read("web/package.json"))
	}
	files := strings.Fields(r.git("show", "--name-only", "--pretty=format:", "HEAD"))
	if len(files) != 1 || files[0] != "web/package.json" {
		t.Errorf("the release commit touched %v, want only web/package.json", files)
	}
	if got := r.remoteGit("tag", "-l"); got != "web-v1.1.0" {
		t.Errorf("tags on the remote = %q, want only web-v1.1.0", got)
	}
}

// A component overrides only the keys it names, so cli keeps its own tag
// template while inheriting branch and remote.
func TestComponentTagOverrideWins(t *testing.T) {
	r := monorepo(t)

	out := r.mustStamp("release", "cli", "patch", "--yes")
	requireContains(t, out, "v0.4.1")
	if strings.Contains(out, "cli-v0.4.1") {
		t.Errorf("the component override did not win:\n%s", out)
	}
}

// Releasing without naming a component is refused, and the refusal lists the
// names rather than making the user go and read the config.
func TestComponentIsRequired(t *testing.T) {
	r := monorepo(t)

	out, code := r.stamp("release", "minor", "--yes")
	if code == 0 {
		t.Fatalf("a release without a component should fail:\n%s", out)
	}
	requireContains(t, out, "cli, web", "name one")
	if got := strings.TrimSpace(r.read("VERSION")); got != "0.4.0" {
		t.Error("something was written despite the error")
	}

	out, code = r.stamp("release", "nope", "minor", "--yes")
	if code == 0 {
		t.Fatalf("an unknown component should fail:\n%s", out)
	}
	requireContains(t, out, `"nope" is not one of them`)
}

func TestComponentCurrentAndSet(t *testing.T) {
	r := monorepo(t)

	if out := r.mustStamp("current", "web"); out != "1.0.0\n" {
		t.Errorf("current web = %q", out)
	}
	if out := r.mustStamp("current", "cli"); out != "0.4.0\n" {
		t.Errorf("current cli = %q", out)
	}
	if _, code := r.stamp("current"); code == 0 {
		t.Error("current without a component should fail in a repository with components")
	}

	r.mustStamp("set", "web", "2.0.0")
	if !strings.Contains(r.read("web/package.json"), `"2.0.0"`) {
		t.Error("set did not write the component's file")
	}
	if got := strings.TrimSpace(r.read("VERSION")); got != "0.4.0" {
		t.Error("set touched the other component")
	}
}

// A CI job knows the tag it was triggered by and nothing else, so verify works
// the component out from the tag.
func TestVerifyFindsTheComponentFromTheTag(t *testing.T) {
	r := monorepo(t)

	out := r.mustStamp("verify", "--tag", "web-v1.0.0")
	requireContains(t, out, "tag matches the committed version", "web")

	if out := r.mustStamp("verify", "--tag", "v0.4.0"); !strings.Contains(out, "matches") {
		t.Errorf("the cli tag was not recognised:\n%s", out)
	}

	out, code := r.stamp("verify", "--tag", "v9.9.9")
	if code == 0 {
		t.Fatalf("an unknown tag should fail:\n%s", out)
	}
	requireContains(t, out, "no component", "cli", "web")
}

// ---------------------------------------------------------------------------
// version locations
// ---------------------------------------------------------------------------

// A YAML field and a nested TOML field are bumped like any other location, and
// the rest of both files survives byte for byte.
func TestReleaseWritesYAMLAndTOMLLocations(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write("charts/app/Chart.yaml", "apiVersion: v2\nname: app        # the chart\nversion: 0.4.0\nappVersion: \"0.4.0\"\n")
	r.write("pyproject.toml", "[project]\nname = \"app\"\nversion = \"0.4.0\"\n\n[tool.ruff]\nversion = \"9.9.9\"\n")
	r.write(".stamp.yml", `
version:
  - VERSION
  - charts/app/Chart.yaml#version
  - charts/app/Chart.yaml#appVersion
  - pyproject.toml#project.version
`)
	r.commitAll("initial")
	r.push()

	// The same file twice, under two different fields, is a legitimate layout.
	out, code := r.stamp("release", "minor", "--yes")
	if code == 0 {
		t.Fatalf("the same path listed twice should be refused:\n%s", out)
	}
	requireContains(t, out, "already listed")

	r.write(".stamp.yml", `
version:
  - VERSION
  - charts/app/Chart.yaml#appVersion
  - pyproject.toml#project.version
`)
	r.commitAll("fix config")
	r.mustStamp("release", "minor", "--yes")

	if got := r.read("charts/app/Chart.yaml"); !strings.Contains(got, `appVersion: "0.5.0"`) ||
		!strings.Contains(got, "# the chart") || !strings.Contains(got, "version: 0.4.0") {
		t.Errorf("Chart.yaml was not edited surgically:\n%s", got)
	}
	if got := r.read("pyproject.toml"); !strings.Contains(got, "version = \"0.5.0\"") ||
		!strings.Contains(got, "version = \"9.9.9\"") {
		t.Errorf("pyproject.toml was not edited surgically:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// config compatibility
// ---------------------------------------------------------------------------

// The superseded source/mirrors shape keeps working, and says so once, on
// stderr, so a piped `stamp current` is unaffected.
func TestLegacyConfigStillReleasesWithANotice(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write("package.json", "{\n  \"version\": \"0.4.0\"\n}\n")
	r.write(".stamp.yml", `
project:
  name: legacy
version:
  source:
    type: file
    path: VERSION
  mirrors:
    - type: json
      path: package.json
      field: version
`)
	r.commitAll("initial")
	r.push()

	out := r.mustStamp("release", "minor", "--yes")
	requireContains(t, out, "still uses version.source", "stamp migrate", "Release legacy")
	if got := strings.TrimSpace(r.read("VERSION")); got != "0.5.0" {
		t.Errorf("VERSION = %q, the superseded shape stopped working", got)
	}
}

// migrate rewrites the old shape into the list form, saying the same thing.
func TestMigrateRewritesTheConfig(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write("package.json", "{\n  \"version\": \"0.4.0\"\n}\n")
	r.write(".stamp.yml", `
project:
  name: legacy
version:
  source:
    type: file
    path: VERSION
  mirrors:
    - type: json
      path: package.json
release:
  branch: trunk
  tag: "{{version}}"
  push: false
`)
	r.commitAll("initial")

	r.mustStamp("migrate")
	config := r.read(".stamp.yml")
	requireContains(t, config, "project: legacy", "  - VERSION", "  - package.json#version",
		"branch: trunk", `tag: "{{version}}"`, "push: false")
	if strings.Contains(config, "mirrors:") {
		t.Errorf("the superseded shape survived:\n%s", config)
	}

	// Everything it said before, it still says.
	r.commitAll("migrate")
	out := r.mustStamp("release", "minor", "--yes", "--no-fetch", "--branch", "main")
	if strings.Contains(out, "still uses version.source") {
		t.Error("the migrated config is still reported as superseded")
	}
	requireContains(t, out, "Release legacy", "0.5.0")

	if _, code := r.stamp("migrate"); code == 0 {
		t.Error("migrating an already-current config should fail")
	}
}

// init in a monorepo proposes components, one per directory, each tagged apart.
func TestInitWritesComponents(t *testing.T) {
	r := newRepo(t)
	r.write("VERSION", "0.4.0\n")
	r.write("web/package.json", "{\n  \"version\": \"1.0.0\"\n}\n")
	r.commitAll("initial")

	out := r.mustStamp("init", "--yes")
	requireContains(t, out, "Wrote .stamp.yml", "Component", "web", "web-v1.0.0")

	config := r.read(".stamp.yml")
	requireContains(t, config, "components:", "  web:", "web/package.json#version", "{{component}}")

	r.commitAll("add config")
	r.mustStamp("release", "web", "minor", "--yes", "--no-fetch")
	if !strings.Contains(r.read("web/package.json"), `"1.1.0"`) {
		t.Error("the config init wrote does not release")
	}
}

// --file replaces detection outright, so a project stamp cannot guess still
// gets a config in one command.
func TestInitWithExplicitFiles(t *testing.T) {
	r := newRepo(t)
	r.write("app/Chart.yaml", "apiVersion: v2\nappVersion: 0.4.0\n")
	r.commitAll("initial")

	out := r.mustStamp("init", "--yes", "--file", "app/Chart.yaml#appVersion")
	requireContains(t, out, "app/Chart.yaml#appVersion", "0.4.0")
	if !strings.Contains(r.read(".stamp.yml"), "app/Chart.yaml#appVersion") {
		t.Errorf("the location was not written:\n%s", r.read(".stamp.yml"))
	}
}

func TestUnknownCommand(t *testing.T) {
	r := newRepo(t)
	r.seed("0.4.0")
	out, code := r.stamp("bump")
	if code == 0 {
		t.Fatal("an unknown command should exit non-zero")
	}
	requireContains(t, out, "unknown command")
}
