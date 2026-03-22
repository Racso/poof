package version

// Commit and CommitTime are set at build time via -ldflags.
// Defaults are used when building locally without the flags.
var (
	Commit     = "dev"
	CommitTime = "unknown"
)
