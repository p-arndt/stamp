// Package buildinfo holds the version metadata injected at build time.
//
// The values are set with -ldflags -X by the release workflow and the justfile;
// a plain `go build` leaves them at their defaults.
package buildinfo

import "fmt"

var (
	// Version is the released version, e.g. "0.1.0".
	Version = "dev"
	// Commit is the abbreviated commit the binary was built from.
	Commit = "none"
	// Date is the UTC build timestamp.
	Date = "unknown"
)

// String renders the full version line.
func String() string {
	return fmt.Sprintf("stamp %s (commit %s, built %s)", Version, Commit, Date)
}
