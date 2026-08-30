// Package version exposes build identity injected by the release build.
package version

var (
	// Version is the application release version or dev for local builds.
	Version = "dev"
	// Commit is the source revision used for the build.
	Commit = "unknown"
	// BuildDate is the source commit timestamp normalized to RFC3339 UTC.
	BuildDate = "unknown"
)

// String returns a stable, human-readable build identity.
func String() string {
	return "image-updater version=" + Version + " commit=" + Commit + " build_date=" + BuildDate
}
