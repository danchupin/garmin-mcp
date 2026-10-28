# Changelog

## 0.3.0 — 2026-10-28

First public release.

- Local Go stdio server for 26 read-only operations in the official Garmin Personal API.
- Hidden-input login, private local key file and in-memory temporary-token renewal.
- Three MCP tools, exact operation schemas, bounded projections and explicit missing/truncated data.
- Expiring JSON and original activity-file resources without a persistent data store.
- Linux/macOS tests, reproducible release archives, dependency notices and MIT licensing.

Hosted HTTP/OAuth, workout uploads and Windows support are outside this release. Some operations are discoverable but have not been exercised against live accounts. See `docs/verification.md` for validation limits.
