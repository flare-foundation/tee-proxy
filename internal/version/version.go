// Package version carries build metadata stamped into the binary at link time.
package version

// Revision is the VCS revision of the build. Container builds inject it via
// -ldflags "-X github.com/flare-foundation/tee-proxy/internal/version.Revision=<sha>"
// because the .git directory is absent from the build context, so
// debug.ReadBuildInfo cannot recover vcs.revision there. It defaults to "unknown"
// for a plain `go build`, where metrics.buildInfo falls back to the vcs.revision
// build setting instead.
var Revision = "unknown"

// Version is the human-facing release version (a semver tag). Release container builds
// inject it via -ldflags from the Git tag; it defaults to "dev" for a plain `go build`
// and for non-release images.
var Version = "dev"
