# Contributing

Bug reports, documentation improvements and focused pull requests are welcome. By contributing, you agree that your contribution is distributed under the project's MIT license.

## Report a problem

Search existing issues first. Include the release or commit, OS/architecture, MCP client, operation name, expected behavior and a minimal reproduction using synthetic data. Do not post API keys, access tokens, exported resource URIs, account IDs or health records. Report security vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

An upstream failure is not proof of missing data. Keep missing values distinct from zero; preserve Garmin calendar dates and report projection truncation. Do not add undocumented Garmin Connect endpoints or browser-login/scraping fallbacks.

## Build and test

```sh
git clone https://github.com/danchupin/garmin-personal-mcp.git
cd garmin-personal-mcp
make check
make build
```

The Go toolchain is pinned in `go.mod`. Python 3 runs packaging tests. All automated tests use synthetic credentials; a contributor does not need a Garmin account or production data.

Keep changes focused. Add a regression test for nontrivial behavior or a bug fix, update the relevant operation contract/docs, run `make check`, and explain the behavior and validation in your pull request. Keep dependencies on stable tagged releases and commit the resulting `go.mod` and `go.sum`.

## Maintainer release procedure

1. Update the server version in `internal/mcpserver/server.go`, installation commands and `CHANGELOG.md`.
2. Run `make check`, govulncheck and a redacted secret scan; review the release source archive.
3. Push an annotated `v`-prefixed version tag matching the server version. The release workflow repeats checks before publishing the archives and checksums.
4. Review the published assets and workflow conclusion. Never attach health exports, `.env`, local configuration or private Git history.

The source archive uses an explicit file allowlist in `scripts/release.py`. Update its packaging tests when adding a new distributed root document. Reproducibility requires the same source, Go/Python toolchains and build settings. A checksum proves file integrity, not independent authorship; current releases are not signed or notarized.
