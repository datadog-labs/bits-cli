# Install Bits

| Platform | Command |
| --- | --- |
| macOS, Linux (Homebrew) | `brew install datadog-labs/pack/bits` |
| macOS, Linux | `curl -fsSL https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.sh \| sh` |
| Windows (PowerShell) | `irm https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.ps1 \| iex` |

The install scripts need no administrator rights. `install.sh` needs `curl` or
`wget`, `tar`, and `sha256sum` or `shasum`; `install.ps1` needs PowerShell 5.1
or later. Both verify the download against the release checksums.

If `irm` fails with "Could not create SSL/TLS secure channel", PowerShell does
not offer TLS 1.2, which GitHub requires. Enable it for the session first:

```powershell
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
irm https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.ps1 | iex
```

## Where it goes

| | macOS, Linux | Windows |
| --- | --- | --- |
| Releases | `~/.local/share/bits/versions/<version>/` | `%LOCALAPPDATA%\bits\versions\<version>\` |
| Active release | `~/.local/share/bits/current` | `%LOCALAPPDATA%\bits\current` |
| `bits` command | `~/.local/bin/bits`, a link to `current/bin/bits` | `%LOCALAPPDATA%\bits\current\bin` on your user `PATH` |

On macOS and Linux, `XDG_DATA_HOME` replaces `~/.local/share` when it is an
absolute path, as the XDG spec requires; keep it a directory only you can
write, since Bits runs what it finds there. An update installs the new release
next to the current one and switches `current` to it, keeping the previous
release.

## PATH

On Windows, the installer adds `bits` to your user `PATH`, so it works in that
window and in new ones. Restart terminals and editors that were already open.

On macOS and Linux, the installer prints what to do when `~/.local/bin` is not
on your `PATH`, for example with zsh:

```sh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> "${ZDOTDIR:-$HOME}/.zshrc"
```

It also warns when another `bits`, such as a Homebrew one, comes first on your
`PATH`. The installer never edits your shell startup files.

## Install a specific release

Set `BITS_VERSION` to a release from v0.0.15 on. Releases before v0.0.15 can
only be installed with Homebrew or from their archive.

```sh
curl -fsSL https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.sh | BITS_VERSION=v0.1.0 sh
```

```powershell
$env:BITS_VERSION = 'v0.1.0'; irm https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.ps1 | iex
```

## Updates

When a release is out, Bits offers to update at startup. Run `bits update` to
update at any time. Set `BITS_NO_UPDATE_CHECK=1` to stop the startup check.
Bits updates the way it was installed: with the install scripts' layout, or
with `brew upgrade` for Homebrew. Homebrew offers a release once its formula is
updated, which can take longer.

## Verify the installer

The commands run the scripts in
[`scripts/`](https://github.com/datadog-labs/bits-cli/tree/main/scripts) on the
default branch. To read a script before running it, download it first:

```sh
curl -fsSLO https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.sh && less install.sh && sh install.sh
```

To run exactly the script you read, replace `HEAD` in the URL with a commit.

The script checks the archive it downloads against the release checksums. To
verify that the archive was built by the release workflow, run
`gh attestation verify` on it, as [RELEASE.md](RELEASE.md) shows.

## Uninstall

```sh
data=$XDG_DATA_HOME; case $data in /*) ;; *) data=$HOME/.local/share ;; esac
# Remove ~/.local/bin/bits only if it is the link the installer made.
[ "$(readlink ~/.local/bin/bits)" = "$data/bits/current/bin/bits" ] && rm ~/.local/bin/bits
rm -rf "$data/bits"
```

On Windows, delete `%LOCALAPPDATA%\bits`, then search the Start menu for
**Edit environment variables for your account** and remove
`%LOCALAPPDATA%\bits\current\bin` from `Path`.

Uninstalling keeps your sign-in and settings.
