# Bits CLI

Bits CLI is a native terminal client for Datadog Assistant.

## Command line

Run `bits` to open chat. Process-level long flags use conventional double-dash syntax; `-h` is the only shorthand:

```sh
bits [--conversation ID]
bits login [--site SITE] [--client-id ID]
bits logout
bits help [command]
```

Use `bits --help` for the complete command list or `bits help login` for command-specific help. In-TUI slash commands such as `/new`, `/resume`, and `/quit` are a separate interactive command surface.

## Authentication

### Startup login

Run `bits`. When no working OAuth session or complete API/app-key pair is available, Bits opens a site picker automatically. Choose US1, US3, US5, EU1, AP1, AP2, or enter your organization's Datadog subdomain. Bits then opens the regional OAuth flow in your browser and continues into chat after authentication succeeds.

`DD_SITE_URL` skips the picker and starts OAuth for that site. The existing `bits login` command remains a non-TUI escape hatch for staging, GovCloud, scripting, and debugging:

```sh
bits login
```

`bits login` defaults to the US1 production login when `DD_SITE_URL` is unset. Site precedence is `--site`, then `DD_SITE_URL`, then the US1 production default. To start from another Datadog site or a customer subdomain, pass it explicitly:

```sh
bits login --site app.datadoghq.eu
bits login --site acme.us3.datadoghq.com
```

The selected site's domain family determines the public OAuth client automatically: `datad0g.com` uses the staging registration, while commercial `datadoghq.com` and `datadoghq.eu` sites use the production registration. The explicit `--client-id` flag or `BITS_OAUTH_CLIENT_ID` environment variable takes precedence for development and environments without a built-in registration, including GovCloud.

Bits opens Datadog in your browser and completes Authorization Code + PKCE through an ephemeral `127.0.0.1` callback. The callback uses an available OS-selected port; no fixed local port needs to be free. The client ID is public configuration; no client secret is shipped.

The resulting access and rotating refresh tokens are stored in the native OS credential manager:

- macOS: Keychain
- Linux: Secret Service-compatible keyring
- Windows: Credential Manager

On Linux without an available Secret Service, Bits uses `~/.bits-cli/oauth-session.json` with mode `0600`. When the keyring becomes available, the next locked session operation promotes the file session and removes the fallback copy. Other credential-store failures are returned rather than silently switching identities.

Only one OAuth login is active per OS user. Running `bits login` again saves the replacement before best-effort revoking the previous grant.

### Developer and CI fallback

A complete API/app-key pair remains available when no OAuth login is stored:

```sh
export DD_API_KEY=...
export DD_APP_KEY=...
export DD_SITE_URL=https://dd.datad0g.com
bits
```

Authentication selection is deterministic:

1. A stored OAuth login wins, even if API/app keys are present in the environment.
2. A complete `DD_API_KEY` and `DD_APP_KEY` pair is used only when no OAuth session exists.
3. Missing, corrupt, or definitively unrefreshable OAuth starts the login flow; partial API/app-key pairs do not suppress it.
4. Transient credential-store and OAuth refresh failures surface as errors instead of opening a browser or changing identities.

### Logout

```sh
bits logout
```

Logout removes the local session under the same per-user cross-process lock used by refresh and login, then attempts remote token revocation. The lock identity is stable across process environment changes. A remote revocation outage does not restore the locally deleted session.

If Bits reports that the login expired or refresh was rejected, run `bits login` again. An unreadable credential can be cleared with `bits logout` before logging in again. If another process replaced the login with a different site or client, restart Bits to adopt it.

An Assistant HTTP 401 is never retried automatically, especially for the non-idempotent streaming turn. It marks that exact access-token generation stale so the next independently initiated request refreshes safely.
