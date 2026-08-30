// Package version resolves the target version of a release from the current
// version plus a CLI argument ("patch", "minor", "major" or an explicit
// version).
package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	semver "github.com/Masterminds/semver/v3"
)

// Bump kinds accepted on the command line.
const (
	Patch = "patch"
	Minor = "minor"
	Major = "major"
	// Final promotes a pre-release to the release it was a candidate for.
	Final = "final"
)

// Parse validates a version string and returns it in comparable form. The
// leading "v" is rejected rather than stripped: the version stored in a
// VERSION file or a package.json is always bare, and silently accepting "v1.2.3"
// would let it leak into the file.
func Parse(s string) (*semver.Version, error) {
	if strings.HasPrefix(s, "v") || strings.HasPrefix(s, "V") {
		return nil, fmt.Errorf("version %q must not carry a leading %q; the tag gets its prefix from the tag template", s, s[:1])
	}
	v, err := semver.StrictNewVersion(s)
	if err != nil {
		return nil, fmt.Errorf("version %q is not valid semver: %w", s, err)
	}
	return v, nil
}

// Resolve turns a CLI argument into a concrete version string. A bump keyword
// is applied to current; anything else is parsed as an explicit version.
//
// Bumping deliberately drops any pre-release and build metadata on the current
// version, and a bump off a pre-release lands on the release that series was
// for rather than past it: patch on 1.3.0-rc.1 is 1.3.0, not 1.3.1. That is
// Masterminds' IncPatch, and it is the right reading: the base version was
// never released, so there is nothing to patch yet. The larger bumps do move
// on (minor on 1.3.0-rc.1 is 1.4.0, skipping 1.3.0 entirely), so "final" is
// the way to promote a candidate: it drops the pre-release and nothing else,
// and says so at the call site instead of relying on the reader knowing which
// bump happens to be a no-op.
func Resolve(current, arg string) (string, error) {
	cur, err := Parse(current)
	if err != nil {
		return "", fmt.Errorf("current version: %w", err)
	}

	switch arg {
	case Patch:
		return bare(cur.IncPatch()), nil
	case Minor:
		return bare(cur.IncMinor()), nil
	case Major:
		return bare(cur.IncMajor()), nil
	case Final:
		if cur.Prerelease() == "" {
			return "", fmt.Errorf("%s is not a pre-release, so there is nothing to promote", current)
		}
		v, _ := cur.SetPrerelease("")
		v, _ = v.SetMetadata("")
		return bare(v), nil
	case "":
		return "", fmt.Errorf("no version given (use patch, minor, major, final or an explicit version)")
	}

	next, err := Parse(arg)
	if err != nil {
		return "", err
	}
	return next.String(), nil
}

// Compare reports whether next is a valid successor of current: strictly
// greater, or exactly equal. Equality is allowed because of the first-release
// case, where the version file already holds the version being tagged; the
// caller decides what to do with it.
func Compare(current, next string) (equal bool, err error) {
	cur, err := Parse(current)
	if err != nil {
		return false, fmt.Errorf("current version: %w", err)
	}
	nxt, err := Parse(next)
	if err != nil {
		return false, err
	}
	switch {
	case nxt.GreaterThan(cur):
		return false, nil
	case nxt.Equal(cur):
		return true, nil
	default:
		return false, fmt.Errorf("target version %s is lower than the current version %s", next, current)
	}
}

// bare renders a version without a "v" prefix.
func bare(v semver.Version) string { return v.String() }

// DefaultPreID is the pre-release identifier used when neither the command line
// nor the config names one.
const DefaultPreID = "beta"

// preIDPattern is the semver rule for a pre-release identifier, minus the
// counter stamp appends: dot-separated alphanumerics and hyphens.
var preIDPattern = regexp.MustCompile(`^[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*$`)

// ResolvePre turns a bump keyword into the next pre-release of that bump.
//
// The rule is the one npm, uv and hatch all share: a bump keyword always bumps,
// applied to the current version with its pre-release stripped off, and the
// counter then starts at 1. It is never absorbed, so the same command means the
// same thing wherever it is typed:
//
//	1.2.3        + minor, beta -> 1.3.0-beta.1
//	1.3.0-beta.1 + minor, beta -> 1.4.0-beta.1
//	1.3.1-beta.1 + minor, beta -> 1.4.0-beta.1
//	1.3.0-beta.1 + patch, beta -> 1.3.1-beta.1
//	1.3.0-beta.1 + major, beta -> 2.0.0-beta.1
//
// Walking the series that is already running is the *bare* form, with no
// keyword at all. That is the one to put in a justfile, because repeating it
// cuts the next candidate rather than moving the target:
//
//	1.3.0-beta.1 + "",    beta -> 1.3.0-beta.2
//	1.3.0-beta.2 + "",    rc   -> 1.3.0-rc.1     (identifier changed, restart)
//
// Off a stable version the bare form is an error: nothing else says which
// release the series is being cut for.
//
// An empty id falls back to DefaultPreID. Continuing an existing series with
// the identifier it already carries is the caller's job; see ContinuesSeries.
//
// Promoting a pre-release to its stable release is not this function's job:
// that is `stamp release final`, and the plain bumps behave as cargo-release's
// do: patch on 1.3.0-rc.1 is 1.3.0, minor is 1.4.0.
func ResolvePre(current, kind, id string) (string, error) {
	cur, err := Parse(current)
	if err != nil {
		return "", fmt.Errorf("current version: %w", err)
	}
	if id == "" {
		id = DefaultPreID
	}
	if !preIDPattern.MatchString(id) {
		return "", fmt.Errorf("pre-release identifier %q is not valid: use letters, digits, hyphens and dots (e.g. beta, rc, alpha.2)", id)
	}

	base, _ := cur.SetPrerelease("")
	base, _ = base.SetMetadata("")

	switch kind {
	case "":
		if cur.Prerelease() == "" {
			return "", fmt.Errorf("%s is not a pre-release, so there is no series to continue. Say which release the series is for: patch, minor or major", current)
		}
		// Mid-series: keep the base version and move the counter on, unless
		// the identifier changed, which starts the series over.
		if oldID, n := splitPre(cur.Prerelease()); oldID == id {
			return withPre(base, id, n+1)
		}
		return withPre(base, id, 1)
	case Patch:
		base = base.IncPatch()
	case Minor:
		base = base.IncMinor()
	case Major:
		base = base.IncMajor()
	default:
		return "", fmt.Errorf("%q is not a bump; a pre-release is cut from patch, minor or major (an exact version goes to `stamp release <x.y.z>`)", kind)
	}
	return withPre(base, id, 1)
}

// ContinuesSeries reports whether this is the bare form walking the series
// current is already in, rather than a bump opening a new one. The caller needs
// to know because an identifier that was not given on the command line is
// inherited from the series being continued, and only from it. A bump starts a
// series for a different release, and that one starts at the configured
// identifier again.
func ContinuesSeries(current, kind string) bool {
	if kind != "" {
		return false
	}
	cur, err := Parse(current)
	if err != nil {
		return false
	}
	return cur.Prerelease() != ""
}

// PreIDOf returns the identifier of a version's pre-release without its
// counter ("rc" for 1.3.0-rc.2), or "" when there is none.
func PreIDOf(s string) string {
	pre := PreOf(s)
	if pre == "" {
		return ""
	}
	id, _ := splitPre(pre)
	return id
}

// splitPre takes a pre-release apart into its identifier and trailing counter.
// "beta.1" is ("beta", 1); "beta" is ("beta", 0); anything whose last segment
// is not a number is not counted at all.
func splitPre(pre string) (id string, n int) {
	dot := strings.LastIndex(pre, ".")
	if dot < 0 {
		return pre, 0
	}
	n, err := strconv.Atoi(pre[dot+1:])
	if err != nil {
		return pre, 0
	}
	return pre[:dot], n
}

// withPre renders base with the pre-release id.n.
func withPre(base semver.Version, id string, n int) (string, error) {
	v, err := base.SetPrerelease(fmt.Sprintf("%s.%d", id, n))
	if err != nil {
		return "", err
	}
	return bare(v), nil
}

// PreOf returns the pre-release part of a version, or "" when it has none or
// is not parseable, because it is for display and an unreadable version is not an
// error here.
func PreOf(s string) string {
	v, err := Parse(s)
	if err != nil {
		return ""
	}
	return v.Prerelease()
}
