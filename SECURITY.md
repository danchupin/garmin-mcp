# Security policy and model

## Reporting a vulnerability

Report vulnerabilities privately using [GitHub private vulnerability reporting](https://github.com/danchupin/garmin-personal-mcp/security/advisories/new). Do not open a public issue for an unpatched vulnerability. Include affected versions, impact and a minimal reproduction with synthetic credentials and records. Never include a real Garmin key, access token, account identifier, health record or export URI.

Only the latest published release receives security fixes. This is a community project without a guaranteed response time. An independent security audit has not been performed.

## Trust boundary

This is a local stdio server launched by a trusted MCP client as the current macOS/Linux user. It does not open a listening port, host remote users, provide HTTP/OAuth or use a user database. Trust your MCP client and the code it can run locally.

## Credentials

`login` reads a Personal API key with terminal echo disabled, validates it against the fixed official HTTPS Garmin endpoint, and atomically saves a private file (`0600`) in a private directory (`0700`). Symlink targets and group/world-accessible credential files are rejected. Reads check regular-file type, permissions, size and inode identity before and after opening.

Only the official Garmin token endpoint receives the key; redirects are disabled. It is not included in tool arguments, schemas, responses or diagnostics. Temporary access tokens remain in RAM and renew on demand. The local source is bound to `local/stdio`; tool arguments cannot choose an identity or inject credentials.

The key file is not encrypted and is readable by other processes with the same OS-user privileges, including locally executed agent code. It does not protect against root, OS compromise, memory dumps or a compromised trusted client. Go/TLS may create additional in-memory copies; physical zeroization of every copy is not guaranteed.

Replace a key with `login` and restart MCP. If compromised, revoke it in Garmin Connect. Replacing the local file does not revoke the old key upstream.

## Data and exports

Responses and downloads exist in RAM while a request is processed. The server does not persist health history, results, export files or user credentials in a database. The MCP client may retain returned data in its own history or logs, outside this server's control.

Export links contain an encrypted operation description, owner and expiry, without Garmin credentials. go-jose uses JWE `dir` / `A256GCM`, without compression. A random key is generated per process and is not saved. Links expire within ten minutes, stop working after restart, and recheck owner, scopes, expiry and parameters before every upstream fetch.

JSON responses are limited to 4 MiB, files to 25 MiB and tool previews to 16 KiB. Timeouts and concurrency limits are enforced. Arbitrary URLs, redirects and unsupported operations are rejected. Garmin content is untrusted data; the server does not execute it or interpret it as instructions. Upstream resource failures expose only a safe error code, not raw bodies.

## Distribution

Release archives use an explicit source allowlist and include relevant license notices. They exclude Git history, `.env`, `.openai`, outputs, data, keys and Codex settings. `dist/` and `bin/` are Git-ignored.

Secret scanning, tests and dependency vulnerability checks reduce risk but do not establish absence of vulnerabilities. Review changes and release sources before publication. Binaries are not signed or notarized; download from this repository and compare published checksums. The public repository starts with a clean source snapshot, without the earlier hosting experiments or their history.
