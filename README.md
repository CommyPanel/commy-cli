# commy

`commy` controls a [CommyPanel](https://github.com/commypanel/panel) server from your own machine or from
CI, through the panel's HTTPS API: deploy, start, stop, restart, read logs and set environment variables.

It is **not** a shell on the server and needs no root or SSH. It can only do what the API token you give it
allows, and nothing the API does not offer (no terminal, no volume files, no passwords, no users).

## Install

```bash
curl -fsSL https://github.com/commypanel/commy-cli/releases/latest/download/install.sh | bash
```

The script downloads the binary for your system, checks its SHA-256 checksum against the release and
installs it to `~/.local/bin` (`COMMY_INSTALL_DIR` changes that, `COMMY_VERSION` picks a version). You can
also download a binary and `SHA256SUMS` from the [releases](https://github.com/commypanel/commy-cli/releases)
page yourself and check it with `sha256sum -c`. Builds exist for macOS, Linux and Windows (amd64 and arm64).

## Sign in

Make a token in the panel under **Settings > API tokens**: give it only the abilities you need, limit it to
the apps you need, and let it expire. Then:

```bash
read -rs COMMY_TOKEN && export COMMY_TOKEN     # type or paste the token; it is not shown or kept in history
commy login --url https://panel.example.com
```

`login` checks the token and saves the address and token in `commy/config.json` in your config folder,
readable only by you. In CI, skip `login` and set `COMMY_URL` and `COMMY_TOKEN` from your secrets.

The token is **never** taken as a command-line argument (it would end up in the process list and the shell
history), and a saved token is only ever sent to the address it was saved with.

## Commands

| Command | What it does | Token ability |
|---|---|---|
| `commy whoami` | what the token is, may do, until when | any |
| `commy apps` / `commy apps show <app>` | list apps / one app and its latest deployment | `apps:read` |
| `commy deploy <app>` | deploy and wait for the result | `apps:deploy` |
| `commy apply <app>` | apply changed variables, volumes and links | `apps:deploy` |
| `commy deployments <app>` / `commy deployment <app> <n> [--log]` | history / one deployment | `apps:read` |
| `commy start`, `stop`, `restart <app>` | control a running app | `apps:control` |
| `commy logs <app> [--tail 200]` | the last lines of the app's output | `apps:logs` |
| `commy env list <app>` | the names of its variables (values are never shown) | `apps:env` |
| `commy env set <app> <KEY>` | set a variable; the value is read from stdin or `--from-file` | `apps:env` |
| `commy env unset <app> <KEY>` | remove a variable | `apps:env` |
| `commy logout` | forget the saved login | none |

```bash
commy deploy shop                       # waits; exit code 0 when it went live, 3 when it failed or was rolled back
commy deploy shop --no-wait --json      # queue it and print the deployment
printf %s "$STRIPE_KEY" | commy env set shop STRIPE_KEY && commy apply shop
```

Most commands accept `--json`. `--url`, `--ca-file` (for a panel whose certificate comes from a private CA)
and `--allow-http-localhost` (plain http, for a panel on this machine only) work everywhere.

**Exit codes:** `0` ok, `1` error, `2` usage, `3` a deployment failed or was rolled back, `4` still running
when `--timeout` ended (it carries on in the panel).

## Security

- **https only**, and TLS is always verified; there is no "skip verification" switch.
- **Redirects are not followed**, so the token cannot be sent to another address.
- **No secrets on the command line**: the token comes from `$COMMY_TOKEN` or the saved login, variable values
  from stdin or a file. Asking for them in a terminal is refused rather than shown.
- **The config file** is created readable only by you, and a copy that others can read is refused.
- **Output is cleaned.** App logs, commit messages and error text are not under your control, so terminal
  control characters in them (which can retitle a window, hide text or write to the clipboard) are shown
  as `�` instead of reaching the terminal.
- No telemetry and no self-update.

Report a vulnerability privately, as described in [SECURITY.md](SECURITY.md).

## Development

```bash
go vet ./... && go test -race ./...
scripts/build.sh 0.1.0        # builds every platform into dist/ with SHA256SUMS
```

The CLI is built on [commypanel-go](https://github.com/commypanel/commypanel-go). To work on both at once, put the two
repositories in one folder and add a `go.work` file there (`use ./commypanel-go ./commy-cli`).

MIT licensed.
