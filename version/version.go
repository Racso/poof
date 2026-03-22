package version

// Commit and BuildTime are set at build time via -ldflags.
// Defaults are used when building locally without the flags.
var (
	Commit    = "dev"
	BuildTime = "unknown"
)
