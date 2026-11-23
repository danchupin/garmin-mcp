# Changelog

## Unreleased

- Add `activity_sets`: decodes FIT `set` messages from the original activity file (same endpoint and `activities:export` scope as `activity_file`) and returns per-set type, exercise category, repetitions, weight, duration, time window and heart rate as JSON.
- Add the `github.com/muktihari/fit` dependency for FIT decoding.

## 0.3.1 — 2026-10-28

- Rename the repository, Go module and MCP implementation to `garmin-mcp`.
- Update installation commands and project links for the new module path.
- State that only personal mode is currently supported and all Garmin access uses the official Garmin Connect Personal API.
- Keep documentation and contribution guidance in English; remove the duplicate Russian guide.

The executable name and local credential-file location are unchanged.

## 0.3.0 — 2026-10-28

First public release.

- Local Go stdio server for 26 read-only operations in the official Garmin Personal API.
- Hidden-input login, private local key file and in-memory temporary-token renewal.
- Three MCP tools, exact operation schemas, bounded projections and explicit missing/truncated data.
- Expiring JSON and original activity-file resources without a persistent data store.
- Linux/macOS tests, reproducible release archives, dependency notices and MIT licensing.

Hosted HTTP/OAuth, workout uploads and Windows support are outside this release. Some operations are discoverable but have not been exercised against live accounts. See `docs/verification.md` for validation limits.
