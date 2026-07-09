// Package version holds nyro's Go binary version. It is the single source of
// truth for both the `nyro version` CLI command and any node-visibility
// reporting (e.g. config-sync's Subscribe.app_version) — reading it here
// beats debug.ReadBuildInfo(), which only carries a real semver for `go
// install pkg@version` builds and reports "(devel)" for a plain `go build`.
package version

// Version is the Go project's release version, independent of go/webui's own
// package.json version and of the separate Rust workspace this project was
// originally forked from.
const Version = "2.0.0"
