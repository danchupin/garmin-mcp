# Garmin MCP

A local, read-only MCP server written in Go. Connect your own Garmin data to Codex or another MCP client that supports stdio.

**Only personal mode is currently supported.** Each local process reads one user's own account through the **official Garmin Connect Personal API**. Business API integrations and shared multi-user servers are not implemented. All Garmin requests use official API endpoints; there is no scraping, browser-login flow or unofficial Garmin client.

[Releases](https://github.com/danchupin/garmin-mcp/releases) · [Security](SECURITY.md) · [Contributing](CONTRIBUTING.md)

- **Official API:** 26 allowlisted GET operations for wellness, activities and original activity files.
- **Local credentials:** enter your Personal API key once in a terminal with hidden input. Temporary access tokens renew automatically.
- **Small tool catalogue:** three tools, field selection, bounded previews and explicit truncation metadata.
- **No hosted service:** one Go binary, stdio, no database or persistent health-data cache.

Independent project; not affiliated with or endorsed by Garmin. The MIT license covers this software, not access to Garmin services. Obtain your own key and follow [Garmin's Personal API terms and eligibility](https://developer.garmin.com/gc-developer-program/personal/personal-faq/).

## Install

Download the archive for your platform from [Releases](https://github.com/danchupin/garmin-mcp/releases/latest), compare its SHA-256 with `SHA256SUMS`, and extract it into a directory you control. macOS arm64/amd64 and Linux arm64/amd64 builds are provided. Release binaries are not signed or notarized.

Alternatively, install from the published Go module:

```sh
GOBIN="$HOME/.local/bin" go install github.com/danchupin/garmin-mcp/cmd/garmin-mcp@v0.3.1
"$HOME/.local/bin/garmin-mcp" login
codex mcp add garmin -- "$HOME/.local/bin/garmin-mcp" stdio
```

Go selects the toolchain required by the module. Restart the `garmin` MCP server in your client after changing its configuration. The default scopes are `wellness:read activities:read`.

For a source checkout:

```sh
git clone https://github.com/danchupin/garmin-mcp.git
cd garmin-mcp
make build
./bin/garmin-mcp login
codex mcp add garmin -- "$PWD/bin/garmin-mcp" stdio
```

`login` verifies the key directly with Garmin before saving it. Never paste the key into a chat, tool argument, URL or issue. There is no setup website, account registration or OAuth provider. Running the binary without arguments also starts stdio.

## Upgrade from v0.3.0

The repository and Go module are now named `github.com/danchupin/garmin-mcp`. Use the v0.3.1 installation command above. The executable name, stdio configuration and local credential-file location are unchanged.

## Credentials and configuration

| Variable | Meaning |
| --- | --- |
| `GARMIN_PERSONAL_API_KEY_FILE` | Optional absolute path to a private key file; configure the same path for `login` and your MCP client |
| `GARMIN_REGION` | `global` by default; `china` is also implemented |
| `GARMIN_SCOPES` | Space-separated operation groups; defaults to `wellness:read activities:read` |

Default key location:

- macOS: `~/Library/Application Support/GarminMCP/personal-api-key`.
- Linux: `$XDG_CONFIG_HOME/GarminMCP/personal-api-key`, or `~/.config/GarminMCP/personal-api-key` when `XDG_CONFIG_HOME` is unset.

The key directory is private (`0700`) and the file is private (`0600`). Other processes running as your operating-system user can still read it. This is not an OS keychain or an encrypted vault.

Only the official fixed HTTPS Garmin hosts receive credentials; redirects are disabled. Temporary access tokens stay in RAM. The next request renews a token when no more than two minutes remain. Parallel requests share one renewal; a failed exchange has a one-minute retry cooldown. Garmin does not issue a refresh token for this flow.

To replace or extend a key, repeat `login` and restart MCP. If a key is compromised, revoke it in Garmin Connect; replacing the local file alone does not revoke it.

For integrations with a secret manager, `GARMIN_PERSONAL_API_KEY` is also accepted. A manually supplied temporary token can use `GARMIN_ACCESS_TOKEN_FILE` or `GARMIN_ACCESS_TOKEN`; this mode does not renew automatically. Do not combine key/token modes or file/value inputs.

To enable every implemented group:

```sh
codex mcp add garmin --env GARMIN_SCOPES='wellness:read activities:read activities:export body:read nutrition:read womens_health:read profile:read fitness_age:read' -- "$HOME/.local/bin/garmin-mcp" stdio
```

These scopes restrict this MCP gateway; they do not reduce the permissions of the underlying Garmin credential.

## Tools

| Tool | Purpose |
| --- | --- |
| `connection_status` | Check local credential availability, temporary-token expiry and region without returning secrets; may exchange the key, but does not validate every data operation |
| `list_garmin_capabilities` | Discover permitted operations; `operation` returns the exact parameter schema |
| `read_garmin` | Read an operation with `fields`, `max_items` and `offset`, or request a full export |

Covered groups include sleep, HRV, heart rate, stress, Body Battery, steps, hydration, SpO₂, weight, nutrition, fitness age, menstrual health, profile settings and activities. The [contract snapshot](docs/personal-api-contract.json) describes the implemented operation parameters. Operation availability does not imply that your account or device has data for it.

Example prompts:

- “Show sleep, HRV, resting heart rate and stress coverage for the last 14 completed days. Keep missing values distinct from zero.”
- “List yesterday's activities, then show the duration, distance and heart rate of my run.”
- “Show today's wellness measurements with the last synchronization time. Distinguish partial-day data from final daily totals.”

Discover an operation's schema before calling it. Keep Garmin calendar dates in the user's timezone. Missing values remain missing. Preview limits are 16 KiB, depth 8, 24 keys per object and 50 array items; `projection` reports omitted paths and array pages. Selected array fields retain aligned rows. Integers outside the safe JSON-number range are represented as strings.

`export_json: true` and `activity_file` return MCP resource links. `resources/read` fetches fresh data from Garmin rather than reading a stored snapshot. Links require the current process and matching scopes, expire within ten minutes and stop working after a restart. Full binary exports require a client supporting MCP resources and messages up to 36 MiB. The server does not unpack or execute downloaded files.

MCP versions `2026-07-28` and `2025-11-25` are supported. The newer protocol uses short text plus structured content; the older protocol receives a JSON text fallback.

## Development and releases

Go 1.27.3 is pinned as the toolchain; the language minimum is Go 1.26. Python 3 is needed only for release packaging and its tests.

```sh
make check
make build
make release
```

Tests use synthetic credentials and do not require a real key. CI runs tests with the race detector on Linux and macOS, static checks, module verification, secret scanning and govulncheck. See [validation coverage](docs/verification.md) for what live tests establish and what remains unverified.

`make release` creates four binary archives, a source archive and `SHA256SUMS` under `dist/`. Binary archives include dependency and Go license notices. Source archives exclude Git history, local settings, credentials and health data. Build paths and VCS metadata are omitted; archive entry order and timestamps are fixed. Reproduce a release with identical sources, Go/Python toolchains and build settings.

## Limits

This release is for local stdio clients. Hosted/mobile ChatGPT connectivity, HTTP, multiple remote users and Windows are outside its scope. The current Codex desktop integration has been exercised; other clients need their own compatibility check.

Only reads are implemented. Workout-plan upload and watch delivery are not provided. Garmin JSON responses are limited to 4 MiB and activity downloads to 25 MiB. A local session allows up to three concurrent operations; excess work receives `BUSY`. Data can differ between Garmin endpoints or update after synchronization; the server does not invent or reconcile measurements.

The process holds credentials and responses in RAM while working. Your MCP client may retain returned health data in its own chat history or logs. Read the [security model](SECURITY.md) before use. An independent security audit has not been performed.

## License

[MIT](LICENSE). Garmin trademarks and Garmin service access are not licensed by this project.
