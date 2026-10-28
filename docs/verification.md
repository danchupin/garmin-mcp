# Validation coverage

This validation applies to personal mode over the official Garmin Connect Personal API. Business API integrations and shared multi-user hosting are not implemented.

## Automated checks

`make check` runs gofmt validation, Go tests with the race detector, go vet, go mod verify and release-packaging tests. Tests use synthetic credentials and require no Garmin key.

Coverage includes official MCP SDK subprocess transport, current/legacy protocol handling, schemas, scopes, identity/credential injection rejection, projections and missing data, output/response limits, export ownership/expiry, safe errors and secret-free diagnostics. Clock-controlled tests cover temporary-token renewal, concurrent exchanges, key rotation and a newly started credential source. Packaging tests check archive ordering, permissions, source inclusion and private-file exclusion.

The GitHub workflows also run govulncheck and a redacted gitleaks scan. Workflow configuration alone is not evidence of a passing remote run; inspect the [Actions results](https://github.com/danchupin/garmin-mcp/actions) for the exact commit.

## Live smoke tests

Before the first public release, a maintainer account was used to exercise the installed macOS arm64 binary through the official MCP Go SDK and Codex. No real credentials, account identifiers, health values or downloaded files are included in this repository or its test fixtures.

Checked paths: credential exchange, three-tool discovery, the 26-operation catalogue, a multi-day wellness window, several activity types, full JSON resources, a ZIP containing an original FIT file, and a fresh data read after process restart without re-entering the key. Old-process export links were rejected. Live coverage of a subset of operations does not establish that every operation works for every account, device or region.

A live download exposed an HTTP 406 caused by overly narrow Accept negotiation. The client now sends `Accept: */*` and includes a ZIP-response regression test. Unknown resource URLs remain hidden; upstream failures return safe codes instead of masking every error as Resource not found.

## Limits

No real 24-hour expiry soak has been performed; renewal is checked with controlled clocks. China endpoints and other MCP clients have not been live-tested. macOS arm64 has been exercised locally; Linux/macOS execution coverage for a particular commit is shown by its CI run. Windows, hosted/mobile ChatGPT and workout uploads are not supported by this release.

Dependency checks and secret scans do not replace an independent security audit. Current dependency versions are pinned; maintainers must periodically review updates. Release checksums are not signatures or notarization.
