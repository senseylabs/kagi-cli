# Kagi CLI

A CLI tool for managing secrets in Kagi. Supports Keycloak Device Authorization Grant for interactive login and Personal Access Tokens (PAT) for CI/CD pipelines.

## Installation

| Method | Platform | Command |
|---|---|---|
| Homebrew | macOS, Linux | `brew install senseylabs/tap/kagi` |
| curl (install script) | macOS, Linux | `curl -sSf https://get.kagi.pw \| sh` |
| Scoop | Windows | coming soon |
| winget | Windows | coming soon |
| Chocolatey | Windows | coming soon |
| Direct download | macOS, Linux, Windows | [GitHub Releases](https://github.com/senseylabs/kagi-cli/releases) |

### Homebrew

```bash
brew tap senseylabs/tap
brew install kagi
```

### curl (macOS / Linux)

```bash
curl -sSf https://get.kagi.pw | sh
```

This downloads the latest release for your OS/architecture, verifies its
checksum, and installs `kagi` to `/usr/local/bin` (falling back to
`~/.local/bin` if that isn't writable). See `install.sh` in this repo.

### Windows

Scoop, winget, and Chocolatey packages are not published yet (coming soon).
Until then, install with PowerShell:

```powershell
iwr https://get.kagi.pw/install.ps1 -useb | iex
```

or download the `windows_amd64`/`windows_arm64` `.zip` directly from the
[releases page](https://github.com/senseylabs/kagi-cli/releases) and put
`kagi.exe` on your `PATH`.

### Direct download

Prebuilt binaries for macOS, Linux, and Windows (amd64 + arm64), plus
`.deb`/`.rpm`/`.apk` packages for Linux, are attached to every
[GitHub release](https://github.com/senseylabs/kagi-cli/releases).

## Usage

### Login

```bash
kagi login
```

Opens your browser for Keycloak authentication. Stores credentials in the macOS Keychain (or `~/.kagi/credentials` on Linux).

After login, the CLI resolves your organization membership:

- **One organization** — it is auto-selected and you are told which.
- **Multiple organizations** — they are listed; pick one with `kagi org use <slug>`.
- **None** — you are prompted to join/create an organization first.

### Organizations

Kagi is multi-organization. Human (JWT) commands act within a single **active
organization**, sent to the API as the `X-Organization-ID` header (the org UUID).

```bash
# List the organizations you belong to (the active one is marked with *)
kagi org list

# Set the active organization by slug
kagi org use sensey

# Show the active organization
kagi org current
```

The active organization (slug + UUID) is persisted to `~/.kagi/config.yaml`.
If no organization is selected, org-scoped commands fail with a clear message
asking you to run `kagi org use <slug>`.

> **CI / `KAGI_TOKEN` (PAT):** a Personal Access Token is bound to one
> organization server-side. PAT requests therefore send **no** org header and
> need **no** `kagi org use` step — `KAGI_TOKEN=vv_... kagi pull ...` keeps
> working with zero extra flags. Sending a mismatched `X-Organization-ID` with a
> PAT is rejected by the backend (403), so the CLI never sends one. The
> `kagi org` commands are JWT-only and refuse to run when `KAGI_TOKEN` is set.

### Setup

Interactive setup wizard to configure your project and environment:

```bash
kagi setup
```

### Personal environments (`--personal`)

`--personal` targets your own personal environment for an app — sugar for
`--env personal`. It is available on `run`, `pull`, and the `secrets`
subcommands:

```bash
kagi run --personal -- npm run dev
kagi pull --personal --output .env
kagi secrets list --personal
```

Personal environments are **user-scoped**: they require an interactive
`kagi login` (JWT). A Personal Access Token (`KAGI_TOKEN`, used in CI) is
rejected with a clear error and never falls back to a shared environment.

**Fallback (run/pull only).** Not every app has a personal environment. When
you pass `--personal` to `run` or `pull` and the app has none, the CLI falls
back to the environment in your `kagi.yaml` and prints a warning to **stderr**
(stdout stays a clean `KEY=VALUE` stream for `pull`):

```
warning: app "/clients/fepatex/api" (…) has no "personal" environment; falling back to "local" from kagi.yaml
```

The `secrets` subcommands (`set`, `get`, `delete`, `list`, `envs`) are
**strict**: `--personal` against an app with no personal environment is a hard
error, never a silent redirect — writing to a shared environment by accident
would affect every developer who pulls it. Naming the environment explicitly
with `--env personal` is likewise always strict, even on `run`/`pull`.

### Pull secrets

```bash
# To stdout
kagi pull --project my-app --env production

# To a file
kagi pull --project my-app --env development --output .env

# As JSON
kagi pull --project my-app --env staging --format json
```

### Run a command with secrets injected

```bash
kagi run -- npm start
```

### Manage secrets

```bash
# Set one or more secrets (KEY=VALUE pairs)
kagi secrets set DATABASE_URL=postgres://... --project my-project --app my-app --env production

# Import secrets from an .env file
kagi secrets set --from-file=.env --project my-project --app my-app --env production

# Get a single secret (decrypted)
kagi secrets get DATABASE_URL --project my-project --app my-app --env production

# List all secrets (masked)
kagi secrets list --project my-project --app my-app --env production

# Delete a secret
kagi secrets delete DATABASE_URL --project my-project --app my-app --env production
```

### Browse the hierarchy

`kagi secrets` is flag-driven:

```bash
# List all projects
kagi secrets

# List apps in a project
kagi secrets -p my-project

# List masked secrets for an (app, env) pair
kagi secrets -p my-project -a my-app -e production
```

Use `kagi secrets env list -p my-project` to list environments; bare `kagi secrets -p my-project` lists apps.

### Manage projects, apps, environments

```bash
# Projects
kagi secrets project create --name my-project --description "..."
kagi secrets project delete --name my-project

# Apps (scoped to a project)
kagi secrets app create -p my-project --name my-app --description "..."
kagi secrets app delete -p my-project --name my-app

# Environments (scoped to a project)
kagi secrets env list   -p my-project
kagi secrets env create -p my-project --name Production --slug production
kagi secrets env delete -p my-project --slug production
```

## Configuration

### Global flags

| Flag | Env var | Default |
|------|---------|---------|
| `--api-url` | `KAGI_API_URL` | `https://api.kagi.pw` |
| `--issuer` | `KAGI_KEYCLOAK_ISSUER` | `https://auth.kagi.pw/realms/kagi` |

### Config file (`.kagi.yaml`)

Place in the current directory or `~/.kagi/config.yaml`:

```yaml
api-url: https://api.kagi.pw
project: my-project
environment: development
organization: sensey                                    # active org slug (display)
organization-id: 00000000-0000-0000-0000-000000000000   # active org UUID (header)
```

The `organization` / `organization-id` keys are managed by `kagi org use` and
`kagi login`; you normally don't edit them by hand.

### CI/CD

Set `KAGI_TOKEN` environment variable to a Personal Access Token for non-interactive use:

```bash
export KAGI_TOKEN=vv_your_token_here
kagi pull --project my-app --env production
```

## License

MIT
