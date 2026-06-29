// Package version is the single source of truth for the act release version.
//
// Binary holds the human-readable release version of the act binary. It
// defaults to "dev" for local builds and is STAMPED at release time by CI via
// the linker:
//
//	go build -ldflags "-X github.com/aac/act/internal/version.Binary=1.2.3" ./cmd/act
//
// Under the commit-to-main release model there is no release-time rebuild on a
// developer's machine — the kit's stage-binaries step performs this stamp in
// CI and commits the result — so this stamp is the binaries' only version.
// Because Binary is initialized to a string constant, the -X override takes
// effect; a non-constant initializer would be silently re-overwritten at init
// and the linker stamp would be ignored.
package version

// Binary is the release version, "dev" unless stamped by the release linker.
var Binary = "dev"
