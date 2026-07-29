// Package version resolves the target version of a release from the current
// version plus a CLI argument ("patch", "minor", "major" or an explicit
// version).
package version

import (
	"fmt"
	"strings"

	semver "github.com/Masterminds/semver/v3"
)

// Bump kinds accepted on the command line.
const (
	Patch = "patch"
	Minor = "minor"
	Major = "major"
)

// Parse validates a version string and returns it in comparable form. The
// leading "v" is rejected rather than stripped: the version stored in a
// VERSION file or a package.json is always bare, and silently accepting "v1.2.3"
// would let it leak into the file.
func Parse(s string) (*semver.Version, error) {
	if strings.HasPrefix(s, "v") || strings.HasPrefix(s, "V") {
		return nil, fmt.Errorf("version %q must not carry a leading %q — the tag gets its prefix from the tag template", s, s[:1])
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
// version: a patch bump of 1.2.3-beta.1 is 1.2.4, not 1.2.4-beta.1. A
// pre-release is always requested explicitly (stamp release 1.3.0-beta.1).
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
	case "":
		return "", fmt.Errorf("no version given (use patch, minor, major or an explicit version)")
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
