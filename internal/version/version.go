// Package version holds build information. The release build sets these
// variables with -ldflags.
package version

import "fmt"

// Set at build time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns the one-line version text.
func String() string {
	return fmt.Sprintf("gomada %s (commit %s, built %s)", Version, Commit, Date)
}
