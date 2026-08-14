package cli

// version and commit are the client's release identity, printed by
// `wt --version` as "wt <version> (<commit>)". dist/build.sh overrides
// both with -ldflags -X from the git describe/rev-parse values that name
// the archive (the same version the archive filename carries), so what is
// installed and what replaced it are the same two strings a person sees
// on the archive. The defaults are the fallback for a bare
// `go build ./cmd/wt` (RELEASE.md, "Binary versioning"; the wtd half of
// the pair lives in cmd/wtd).
var (
	version = "0.2.0"
	commit  = "unknown"
)
