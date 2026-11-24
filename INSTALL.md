# Install Bits

| Platform | Command |
| --- | --- |
| macOS, Linux (Homebrew) | `brew install datadog-labs/pack/bits` |
| macOS, Linux | `curl -fsSL https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.sh \| sh` |
| Windows (PowerShell) | `irm https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.ps1 \| iex` |

The install scripts need no administrator rights. `install.sh` needs `curl` or
`wget`, `tar`, `sha256sum` or `shasum`, and `flock` on Linux (system Perl on macOS); `install.ps1` needs PowerShell 5.1
or later in FullLanguage mode (the default), with .NET available to create a
Windows junction without administrator rights. Both verify the archive against
the release checksums and check the executable version before making it active.

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
The existing PATH value keeps its registry type and expansion behavior. If an
expandable PATH must name an installation containing literal percent signs,
the installer creates the user variable `BITS_INSTALL_BIN` to preserve them.

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
For a script installation, Bits downloads the complete current installer from
the default branch and runs it with the existing installation root and selected
release. Changing `XDG_DATA_HOME`, `LOCALAPPDATA`, or `BITS_VERSION` does not move
or pin an update. If another installation has switched the active version since
this Bits session started, the updater leaves that selection alone; restart
Bits and update again if needed.

Homebrew installations update with `brew upgrade`. Homebrew offers a release
once its formula is updated, which can take longer.

## Verify the installer

The commands run the scripts in
[`scripts/`](https://github.com/datadog-labs/bits-cli/tree/main/scripts) on the
default branch. To read a script before running it, download it first:

```sh
curl -fsSLO https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.sh && less install.sh && sh install.sh
```

To run exactly the script you read, replace `HEAD` in the URL with a commit.
This pins that invocation only: `bits update` fetches the current default-branch
installer over HTTPS. First installation and updates therefore trust changes
to these scripts as well as the release publishing workflow. Bits does not
independently verify the installer's provenance before executing it.

The script checks the archive it downloads against the release checksums. To
verify that the archive was built by the release workflow, run
`gh attestation verify` on it, as [RELEASE.md](RELEASE.md) shows.

## Uninstall

```sh
data=${XDG_DATA_HOME:-}; case $data in /*) ;; *) data=$HOME/.local/share ;; esac
if root=$(cd -P "$data/bits" 2>/dev/null && pwd); then
    # Remove ~/.local/bin/bits only if it is the link the installer made.
    [ "$(readlink ~/.local/bin/bits)" = "$root/current/bin/bits" ] && rm ~/.local/bin/bits
    rm -rf "$data/bits"
fi
```

On Windows, delete `%LOCALAPPDATA%\bits`, then search the Start menu for
**Edit environment variables for your account** and remove
the Bits entry from `Path` (`%LOCALAPPDATA%\bits\current\bin`, its absolute path,
or `%BITS_INSTALL_BIN%`). If the installer created `BITS_INSTALL_BIN`, remove
that user variable too.

Uninstalling keeps your sign-in and settings.
