# Bits CLI

Bits CLI brings Bits Chat and Datadog tools to your terminal. It can read and
edit local code, run commands, and continue conversations started in Datadog.

> [!NOTE]
> Bits CLI is in preview. Features and interfaces may change.

## Install

On macOS or Linux, install with Homebrew:

```sh
brew tap datadog-labs/pack
brew install datadog-labs/pack/bits
```

Standalone binaries are also available from [GitHub Releases](https://github.com/datadog-labs/bits-cli/releases).

## Get started

Open your project and launch Bits:

```sh
cd your-project
bits
```

On first launch, choose your Datadog site and sign in through your browser.
You need a Datadog account with the Bits Chat Access permission.

<img width="710" height="573" alt="Bits CLI running Bits Chat in the terminal" src="https://github.com/user-attachments/assets/01c89baf-5a23-4e4c-8c78-64b490b6722c" />

## Use Datadog tools and conversations

Datadog tools are built in. Browser sign-in needs no API keys or separate tool
server. Type `@` to find services, monitors, incidents, and dashboards.

- `/resume` selects a saved conversation.
- `/fork` (or `/branch`) copies the saved conversation and continues in the copy.
  It leaves the original conversation and your working directory unchanged.
- `/web` opens the current conversation in Datadog.

You can also resume by ID:

```sh
bits --conversation YOUR_CONVERSATION_ID
```

Run `bits --help` for CLI commands, or type `/` in chat to find chat commands.

## Permissions

By default, interactive Bits asks before reading files, editing files, or running
commands. Choose **Allow** for one action or **Allow for session** to reuse approval:

- A read approval covers reading, listing, and searching files in the workspace.
- A write approval covers creating and editing files in the workspace.
- A command approval covers that exact command and its execution settings.

Use `/permissions` to switch between `manual` (ask for approval) and
`skip-permissions` (full access). To start with full access:

```sh
bits --permissions skip-permissions
```

Full access allows actions without asking for approval.

## Project instructions

Add an `AGENTS.md` to your project to give Bits shared instructions. Bits reads
instruction files in parent directories and the working directory, from the
filesystem root down, including directories outside the Git repository.

In each directory, Bits uses the first readable file in this order:
`AGENTS.override.md`, `AGENTS.md`, `AGENTS.MD`, `CLAUDE.md`, `CLAUDE.MD`.

## Skills

Skills are reusable instructions in a `SKILL.md` file. Bits looks in:

- `.agents/skills/` in the working directory and its parents, up to the repository root.
- Extra directories passed with `--skill`.
- `~/.agents/skills/` for skills shared across projects.

Outside a Git repository, only the working directory, extra directories, and
home skills are searched. For duplicate names, the nearest project skill wins,
then extra directories in flag order, then home skills.

Create `.agents/skills/review-code/SKILL.md`:

```markdown
---
name: review-code
description: Review code changes for correctness and missing tests.
---

Read the diff and surrounding code. Report issues with file references and
explain their impact.
```

The header needs a nonempty `description`. If `name` is omitted, Bits uses the
folder name. Names can contain lowercase letters and numbers separated by
hyphens or colons, up to 64 characters.

Type `/` in chat to find a skill, or run it with optional arguments:

```text
/skill:review-code focus on error handling
```

Bits shares skill names and descriptions with the assistant. Add
`disable-model-invocation: true` to the header to leave a skill out of that list
and invoke it yourself.

To add skill directories, repeat `--skill`:

```sh
bits --skill ./team-skills --skill ~/.claude/skills
```

Start a new conversation after adding or renaming skills to refresh the list.

## Run without interactive chat

Sign in with `bits login`, then run one turn:

```sh
bits run --prompt "Summarize this conversation" --conversation YOUR_CONVERSATION_ID --delivery adeep
```

Omit `--conversation` to start a new conversation. Both `--prompt` and
`--delivery adeep` are required. Pass the prompt as text in the flag; stdin and
positional prompts are not supported. Output is JSONL (one JSON object per line).

Actions that need approval are denied by default. Add
`--permissions skip-permissions` to allow them without prompts.
See the [noninteractive guide](internal/headless/adeep/README.md) for output fields,
exit codes, and authentication details.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, tests, and the contribution workflow.

## License

Bits CLI uses the [Apache-2.0 License](LICENSE).
See [LICENSE-3rdparty.csv](LICENSE-3rdparty.csv) for third-party licenses.
