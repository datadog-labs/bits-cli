# Bits CLI

Bits CLI brings Bits Chat and its built-in Datadog tools to your terminal. It
adds coding tools that run in your local workspace, so Bits can inspect and edit
code, run commands, and help implement and verify fixes using your Datadog
context. Start a new conversation or continue one you started in Datadog.

> [!NOTE]
> Bits CLI is currently in preview. Features and interfaces may change.

## Installation

### macOS

Install with Homebrew:

```sh
brew tap datadog-labs/pack
brew install datadog-labs/pack/bits
```

### Linux

Install with Homebrew:

```sh
brew tap datadog-labs/pack
brew install datadog-labs/pack/bits
```

Or download the standalone binary from GitHub Releases:

```sh
VERSION="$(curl -fsS -o /dev/null -w '%{redirect_url}' \
  https://github.com/datadog-labs/bits-cli/releases/latest | sed -n 's|.*/tag/v||p')"
[ -n "$VERSION" ] || { echo "Could not resolve the latest bits release." >&2; exit 1; }
case "$(uname -m)" in
  x86_64)        ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

ASSET="bits_${VERSION}_linux_${ARCH}.tar.gz"
curl -fL \
  "https://github.com/datadog-labs/bits-cli/releases/download/v${VERSION}/${ASSET}" \
  -o "/tmp/${ASSET}"
tar -xzf "/tmp/${ASSET}" bits
sudo install -m 755 bits /usr/local/bin/
rm -f "/tmp/${ASSET}" bits
```

## Get started

Launch Bits from the project you want to work on:

```sh
cd your-project
bits
```

On first launch, choose your Datadog site and sign in through your browser.
You'll need a Datadog account with the Bits Chat Access permission.

<img width="710" height="573" alt="Bits CLI running Bits Chat in the terminal" src="https://github.com/user-attachments/assets/01c89baf-5a23-4e4c-8c78-64b490b6722c" />

## Datadog integration

Sign in to use the Datadog tools available through Bits Chat.
The integration is built in, with no separate Datadog tool server to configure
or API keys to manage for browser sign-in.

Reference services, monitors, incidents, and dashboards directly in your
conversation. Type `@` to search your Datadog entities and attach the relevant
context to your question.

An investigation started in Datadog can continue against your local code.
Use `/resume` to select a conversation, or open it by ID:

```sh
bits --conversation YOUR_CONVERSATION_ID
```

Use `/fork` (alias `/branch`) to copy the current saved conversation and continue
in the copy. The chat stays in place, with a marker at the copied-history boundary
when the server provides it. That marker is restored when you resume the fork.
Forking does not change your working directory or modify the original conversation.

Use `/web` to reopen the active conversation in Datadog.

Bits asks for permission before reading workspace files, editing files, or
executing commands. Run `bits --help` for available commands.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and the contribution workflow.

## License

Bits CLI is released under the [Apache-2.0 License](LICENSE). Third-party components and their licenses are listed in [LICENSE-3rdparty.csv](LICENSE-3rdparty.csv).
