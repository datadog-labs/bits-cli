# Bits CLI

Bits CLI is a native terminal client for Datadog Assistant.

## Command line

Run `bits` to open chat. Process-level long flags use conventional double-dash syntax; `-h` is the only shorthand:

```sh
bits [--permissions MODE] [--conversation ID]
bits --auth api-key --site API_SITE [--permissions MODE] [--conversation ID]
bits run --prompt TEXT --delivery adeep [--model MODEL] [flags]
bits login [--site SITE] [--client-id ID]
bits logout
bits help [command]
```

Use `bits --help` for the complete command list or `bits help login` for command-specific help. The TUI has its own slash commands:

| Command | Description |
| --- | --- |
| `/new`, `/clear` | Start a new conversation. |
| `/resume` | Resume an existing conversation. |
| `/status` | Show the current session status. |
| `/permissions` | Show or switch the permissions mode for this session. |
| `/web` | Open the active conversation in the Datadog web app. |
| `/settings` | Open Assistant settings in the Datadog web app. |
| `/logout` | Sign out from your Datadog account and exit Bits. |
| `/quit`, `/exit` | Exit Bits. |

Local skills appear in the slash menu as `/skill:<name>`. Bits discovers them in
`.agents/skills`, `~/.agents/skills`, and directories passed with `--skill`.

Type `@` after whitespace or punctuation to open a mixed picker of entities and
local-file suggestions. Prefix a query with a supported entity type, such as
`@service:assistant`, to narrow the search. Entity selections are
inserted as typed mentions such as `@dashboard:"Test Dashboard"` and sent as
structured context for the next turn. Editing or deleting the mention removes
that entity from the structured context. File mentions remain plain prompt text.

## Permissions

Interactive `bits` defaults to `manual`: tools that can change your system or
workspace — such as `exec_command`, `write_file`, and `edit_file` — pause with
a permission panel before running. Run `bits --permissions skip-permissions`
to run those tools without asking.

Inside a session, `/permissions` shows the current mode, and
`/permissions manual` or `/permissions skip-permissions` switches it for the
rest of the session. The switch is never persisted: the next launch starts
from the flag's default (`manual`). Switching is rejected while a turn or a
permission request is active, and only typed input can change the mode — the
assistant cannot switch it for you. Session-wide "allow" grants you gave
earlier stay valid when you switch back to `manual`.

`bits run` has no interactive approver, so it denies every gated action by
default (`--permissions deny`). Pass `--permissions skip-permissions` to run
everything without asking.

## Noninteractive run

`bits run` executes exactly one assistant turn without a TUI or interactive login. It writes only versioned JSONL to stdout; diagnostics go to stderr:

```sh
BITS_FAKE_BACKEND=1 bits run --prompt 'random()' --delivery adeep
bits run --prompt "What changed?" --delivery adeep --auth api-key --site https://api.datadoghq.com
```

`--prompt` is required and literal: positional and stdin prompts are not supported. The initial and only delivery is `adeep`, which emits `bits.delivery.adeep` v1 run/round lifecycle, correlated tool calls and results, usage, conversation updates, and one terminal record. Assistant Markdown appears only as `run.finished.response`. Without a working OAuth session, `run` fails fast with a `bits login` hint.

Exit statuses are `0` for a completed turn, `1` for startup/runtime/delivery failure, `2` for command or flag misuse, and `3` when at least one approval gate was denied even though the backend could adjust and finish. On the headless surface a denial comes from a gated action under `--permissions deny`, or from a malformed server approval request; each is returned to the model as an error tool response.

`bits --version` prints the module version when available, otherwise `dev` with the short commit recorded in the build.

## Authentication

### Startup login

Run `bits`. When no working OAuth session is available, Bits opens a site picker automatically. Choose US1, US3, US5, EU1, AP1, AP2, or enter your organization's Datadog subdomain. Bits then opens the regional OAuth flow in your browser and continues into chat after authentication succeeds.

The `bits login` command is the explicit non-TUI login path for choosing a site before chat, including staging, GovCloud, scripting, and debugging:

```sh
bits login --site app.datadoghq.eu
bits login --site acme.us3.datadoghq.com
```

`bits login` also accepts `--client-id` for development and environments without a built-in registration, including GovCloud. It defaults to the US1 production site when `--site` is omitted:

```sh
bits login --site customer.ddog-gov.com --client-id UUID
```

The selected site's domain family determines the public OAuth client automatically: `datad0g.com` uses the staging registration, while commercial `datadoghq.com` and `datadoghq.eu` sites use the production registration. Site and OAuth-client selection are non-secret runtime configuration and are accepted only as flags. The former `DD_SITE_URL` and `BITS_OAUTH_CLIENT_ID` environment overrides are no longer supported; use `--site` and `bits login --client-id` respectively.

Bits opens Datadog in your browser and completes Authorization Code + PKCE through an ephemeral `127.0.0.1` callback. The callback uses an available OS-selected port; no fixed local port needs to be free. The client ID is public configuration; no client secret is shipped.

The resulting access and rotating refresh tokens are stored in the native OS credential manager:

- macOS: Keychain
- Linux: Secret Service-compatible keyring
- Windows: Credential Manager

On Linux without an available Secret Service, Bits uses `~/.bits-cli/oauth-session.json` with mode `0600`. When the keyring becomes available, the next locked session operation promotes the file session and removes the fallback copy. Other credential-store failures are returned rather than silently switching identities.

Only one OAuth login is active per OS user. Running `bits login` again saves the replacement before best-effort revoking the previous grant.

### Developer and CI authentication

API and application keys remain environment variables because they are secrets. Select them explicitly for a deterministic, noninteractive authentication path:

```sh
export DD_API_KEY=...
export DD_APP_KEY=...
bits --auth api-key --site https://api.datadoghq.com
```

Explicit API-key mode requires `--site`, accepts an `api.`-prefixed Datadog API URL or hostname (plus the staging host `dd.datad0g.com`), and does not read, refresh, replace, or delete a stored OAuth session. It never opens a browser or login picker. Both secrets are required; OAuth client selection remains on `bits login`. The mode affects only the current invocation, so a later plain `bits` returns to automatic OAuth selection. This makes authentication noninteractive; `bits` still launches its interactive terminal UI.

Authentication selection is deterministic:

1. `--auth auto` is the default and uses only a stored OAuth session or interactive OAuth login. Ambient API/app keys never change the selected principal.
2. `--auth api-key` bypasses the OAuth credential store and requires `--site` plus the complete `DD_API_KEY` and `DD_APP_KEY` pair.
3. In automatic mode, missing, corrupt, or definitively unrefreshable OAuth starts the login flow.
4. Transient credential-store and OAuth refresh failures surface as errors instead of opening a browser or changing identities.

### Logout

```sh
bits logout
```

Logout removes the local session under the same per-user cross-process lock used by refresh and login, then attempts remote token revocation. The lock identity is stable across process environment changes. A remote revocation outage does not restore the locally deleted session.

Inside the TUI, `/logout` uses the same path and exits after invalidating the current authenticated client. Running `/logout` when no OAuth session is stored is safe.

If Bits reports that the login expired or refresh was rejected, run `bits login` again. An unreadable credential can be cleared with `bits logout` before logging in again. If another process replaced the login with a different site or client, restart Bits to adopt it.

An Assistant HTTP 401 is never retried automatically, especially for the non-idempotent streaming turn. It marks that exact access-token generation stale so the next independently initiated request refreshes safely.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and the contribution workflow.

## License

Bits CLI is released under the [Apache-2.0 License](LICENSE). Third-party components and their licenses are listed in [LICENSE-3rdparty.csv](LICENSE-3rdparty.csv).
