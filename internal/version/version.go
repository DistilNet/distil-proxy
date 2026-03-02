package version

import "fmt"

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Info contains build metadata.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// DefaultInfo returns metadata embedded at build time via ldflags.
func DefaultInfo() Info {
	return Info{
		Version: version,
		Commit:  commit,
		Date:    date,
	}
}

// String renders build metadata for CLI output.
func (i Info) String() string {
	return fmt.Sprintf("version=%s commit=%s date=%s", i.Version, i.Commit, i.Date)
}
