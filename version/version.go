package version

// Number, Commit, and CommitTime are set at build time via -ldflags.
// Defaults are used when building locally without the flags.
var (
	Number     = "dev"
	Commit     = "dev"
	CommitTime = "unknown"
)

// String returns the canonical version string: "v1.2.3   // abcdef @ TIMESTAMP"
func String() string {
	return "v" + Number + "   // " + Commit + " @ " + CommitTime
}
