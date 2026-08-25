# Bits CLI

Bits CLI is a native terminal client for Datadog Assistant.

> **OAuth rollout status:** this branch uses the dedicated org-2 staging client while production registration and regional validation are completed. It is not yet a production release.

## Authentication

### Interactive login

```sh
bits login --site dd.datad0g.com
```

Bits opens Datadog in your browser and completes Authorization Code + PKCE through an ephemeral `127.0.0.1` callback. The callback uses an available OS-selected port; no fixed local port needs to be free.

The resulting access and rotating refresh tokens are stored in the native OS credential manager:

- macOS: Keychain
- Linux: Secret Service-compatible keyring
- Windows: Credential Manager

Credential-store failures are returned rather than falling back silently to another identity. Tokens are not written to plaintext configuration files.

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
3. Partial key pairs are never used.
4. Credential-store errors do not trigger API-key fallback.

### Logout

```sh
bits logout
```

Logout removes the local session under the same per-user cross-process lock used by refresh and login, then attempts remote token revocation. The lock identity is stable across process environment changes. A remote revocation outage does not restore the locally deleted session.

If Bits reports that the login expired or refresh was rejected, run `bits login` again. An unreadable credential can be cleared with `bits logout` before logging in again. If another process replaced the login with a different site or client, restart Bits to adopt it.

An Assistant HTTP 401 is never retried automatically, especially for the non-idempotent streaming turn. It marks that exact access-token generation stale so the next independently initiated request refreshes safely.
