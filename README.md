# Kagi CLI

A CLI for managing secrets and certificates in Kagi. You authenticate **once**
with `kagi login` (Keycloak Device Authorization Grant); scripts and AI agents
then act through that same logged-in session — no token to pass around.

Secrets and certificates live in an organization-scoped **folder tree**. Apps
are addressed by a stable **app ID**, which you resolve once from a folder path
(`kagi setup`) and pin in a per-directory `kagi.yaml`.

## Installation

| Method | Platform | Command |
|---|---|---|
| Homebrew | macOS, Linux | `brew install senseylabs/tap/kagi-cli` |
| curl (install script) | macOS, Linux | `curl -sSf https://get.kagi.pw \| sh` |
| PowerShell | Windows | `iwr https://get.kagi.pw/install.ps1 -useb \| iex` |
| Direct download | macOS, Linux, Windows | [GitHub Releases](https://github.com/senseylabs/kagi-cli/releases) |

### Homebrew

```bash
brew tap senseylabs/tap
brew install kagi-cli
```

### curl (macOS / Linux)

```bash
curl -sSf https://get.kagi.pw | sh
```

Downloads the latest release for your OS/architecture, verifies its checksum,
and installs `kagi` to `/usr/local/bin` (falling back to `~/.local/bin` if that
isn't writable). See `install.sh` in this repo.

### Windows (PowerShell)

```powershell
iwr https://get.kagi.pw/install.ps1 -useb | iex
```

Installs `kagi.exe` into `%LOCALAPPDATA%\Kagi\bin` and adds it to your user
`PATH`. See `install.ps1`. Scoop / winget / Chocolatey packages are not
published yet. You can also download the `windows_amd64` / `windows_arm64`
`.zip` directly from the [releases page](https://github.com/senseylabs/kagi-cli/releases)
and put `kagi.exe` on your `PATH`.

### Direct download

Prebuilt binaries for macOS, Linux, and Windows (amd64 + arm64), plus
`.deb` / `.rpm` / `.apk` packages for Linux, are attached to every
[GitHub release](https://github.com/senseylabs/kagi-cli/releases).

## Authentication model

Kagi has one interactive credential, one non-interactive credential, and no
CLI-minted tokens.

- **Humans** run `kagi login` once. This starts a Keycloak Device Authorization
  Grant, opens your browser, and stores the resulting credentials in your OS
  secret service (macOS Keychain, Linux Secret Service / D-Bus, Windows
  Credential Manager), falling back to a file under `~/.kagi` where no secret
  service is available.

- **Scripts and AI agents** invoke `kagi` on a machine where a human has already
  logged in. They inherit that session — there is nothing to export, no token to
  manage.

- **CI runners** set `KAGI_TOKEN` to an access token minted in the web console.
  When it is set, it *is* the credential: the CLI authenticates with it as a
  bearer token and never touches the stored session, so a pipeline never needs
  to log in. See [Non-interactive auth](#non-interactive-auth-kagi_token) below.

- **The CLI can never create tokens.** There is deliberately no `token create`
  command — the only way a token comes into existence is through the audited web
  console. `kagi token list` / `kagi token revoke` are management-only.

- **Kubernetes workloads** don't log in and don't carry a static token. They
  authenticate with **workload identity** — a service account's projected token,
  exchanged for Kagi access via the operator. You register the cluster and bind
  the service account with `kagi cluster` and `kagi workload` (below); the
  workload itself never runs `kagi login`.

```bash
kagi login    # once, interactively
kagi logout   # clear stored credentials
```

After login the CLI resolves your organization membership (see below).

### Non-interactive auth: `KAGI_TOKEN`

A CI job cannot complete a browser-based device grant, so it authenticates with
a long-lived access token instead:

```yaml
env:
  KAGI_TOKEN: ${{ secrets.KAGI_TOKEN }}
run: kagi pull --app-id "$APP_ID" --env prod --out-file .env
```

Mint the token in the Kagi web console (the CLI deliberately cannot). Then:

- **`KAGI_TOKEN` wins over everything.** When it is set and non-empty, the CLI
  uses it and never reads the stored `kagi login` session — a job that names an
  identity must get that identity, even on a developer laptop that is also
  logged in. `kagi login` warns when it sees `KAGI_TOKEN` set.

- **An empty value means unset.** `KAGI_TOKEN=` (an unpopulated CI secret) falls
  through to the stored session and, failing that, reports "not logged in" —
  rather than sending an empty bearer token and failing as an opaque 401.
  Surrounding whitespace is trimmed.

- **The token carries its own organization.** It is bound to one organization
  server-side, so the CLI does *not* send the `X-Organization-ID` header for it
  and `kagi org list` / `use` / `current` are refused as inapplicable. There is
  nothing to select, and a locally selected org would be rejected as a mismatch.

- **The token carries its owner's permissions.** An access token acts as the
  user who minted it, with that user's grants — it is not a read-only credential
  and it is not narrower than its owner. Give a CI job a token minted by an
  account that holds only the access that job needs, and rotate it.

- **The personal environment is user-scoped.** `--personal` resolves to the
  token owner's personal environment; a machine-type token has none. Under
  `KAGI_TOKEN` the `run`/`pull` fallback to the `kagi.yaml` environment is
  suppressed, so a job asking for `personal` fails loudly instead of quietly
  reading the shared environment.

> **This is a stopgap.** A static token is a long-lived credential sitting in a
> CI secret store. It exists so pipelines are not blocked, and it is intended to
> be superseded by OIDC federation — CI exchanging its own short-lived workflow
> identity token for Kagi access, the way Kubernetes workloads already do via
> `kagi cluster` / `kagi workload`. Expect `KAGI_TOKEN` to be deprecated once
> that path is available.

## Organizations

Kagi is multi-organization. Human commands act within a single **active
organization**, sent to the API as the `X-Organization-ID` header.

```bash
kagi org list       # organizations you belong to (active one marked with *)
kagi org use sensey # set the active organization by slug
kagi org current    # show the active organization
```

On login: a single membership is auto-selected; multiple memberships are listed
for you to pick with `kagi org use <slug>`; none prompts you to join or create an
org first. The selection (slug + UUID) is persisted to `~/.kagi/config.yaml`.
Org-scoped commands fail with a clear message if no organization is selected.

## Binding a directory: `setup` + `kagi.yaml`

Instead of passing an app on every command, bind the current directory once:

```bash
# Interactive: browse for the app and environment
kagi setup

# Non-interactive
kagi setup --path /village/kaizen --env prod
```

`setup` resolves the folder/app **path** to the app's stable **app ID** and
writes a `kagi.yaml` in the current directory:

```yaml
# Kagi binding for this directory. Secrets are addressed by the stable
# app-id; folder-path is a human reference only and is not used for addressing.
folder-path: /village/kaizen
app-id: 7a3c...            # stable — survives app renames and folder moves
environment: prod
organization: sensey                                    # active org slug (display)
organization-id: 00000000-0000-0000-0000-000000000000   # active org UUID (header)
```

Addressing uses `app-id`; `folder-path` is documentation only. Use `-y` to
overwrite an existing `kagi.yaml` without prompting.

> The legacy pre-folder-model `project:` / `app:` keys are no longer used for
> addressing. A `kagi.yaml` that still has them and lacks `app-id` is detected as
> stale and you'll be asked to re-run `kagi setup`. **Do not** add a `project:`
> key by hand — it trips the legacy detector.

### Overriding the binding

Every secrets/pull/run command accepts, in precedence order:

- `--app-id <id>` — the stable machine binding, highest precedence
- `--path/-p <path>` — a folder/app path (e.g. `/village/kaizen`), resolved to an
  app ID
- `--env/-e <slug>` — the environment slug

Any of these override the `kagi.yaml` values for that one invocation.

## Working with secrets

`kagi secrets` browses the folder tree and manages secrets for an app's
environment.

```bash
# Browse the secrets root (folders + apps)
kagi secrets

# Browse a folder
kagi secrets /village

# List an app's environments
kagi secrets envs -p /village/kaizen

# List masked secrets for an (app, env)
kagi secrets list -p /village/kaizen -e prod

# Get one secret, decrypted
kagi secrets get DATABASE_URL -p /village/kaizen -e prod

# Set one or more KEY=VALUE pairs
kagi secrets set DATABASE_URL=postgres://... API_KEY=... -p /village/kaizen -e prod

# Import from an .env file
kagi secrets set --from-file .env -p /village/kaizen -e prod

# Delete a secret (-y to skip confirmation)
kagi secrets delete DATABASE_URL -p /village/kaizen -e prod
```

With a `kagi.yaml` in place you can drop the `-p`/`-e` flags:

```bash
kagi secrets list
kagi secrets get DATABASE_URL
```

### Pull secrets

```bash
# KEY=VALUE to stdout
kagi pull

# to a file (written 0600)
kagi pull --out-file .env

# as JSON or YAML
kagi pull -o json
kagi pull -o yaml

# override the binding
kagi pull -p /village/kaizen -e prod
```

### Run a command with secrets injected

```bash
kagi run -- npm start
kagi run -p /village/kaizen -e prod -- ./deploy.sh
```

Secrets are injected as environment variables into the child process only.

### Personal environments (`--personal`)

`--personal` targets your own personal environment for an app — sugar for
`--env personal`. Available on `run`, `pull`, and the `secrets` subcommands:

```bash
kagi run --personal -- npm run dev
kagi pull --personal --out-file .env
kagi secrets list --personal
```

Personal environments are user-scoped and require an interactive login.
`run`/`pull` fall back to the `kagi.yaml` environment (with a warning on
**stderr**, keeping stdout a clean stream) when an app has no personal
environment; the `secrets` subcommands are **strict** and error instead of
silently redirecting to a shared environment.

## Certificates

`kagi cert` browses a certificate folder tree and manages certificates. A
leading-slash argument is a node path (folder segments then the certificate
slug); anything else matches by name, slug, or id.

```bash
# Browse the certificates root / a folder
kagi cert
kagi cert /sensey

# List every certificate (flat) with its folder path
kagi cert list

# Show a certificate by node path, or by name/slug/id
kagi cert get /sensey/sensey-io-cloudflare-cert
kagi cert get sensey-io-cloudflare-cert

# Reveal the certificate + private key PEM content
kagi cert reveal sensey-io-cloudflare-cert

# Create a certificate in a folder
kagi cert create --name sensey-io --cert-file cert.pem --key-file key.pem --path /sensey

# Update (replace the PEM material)
kagi cert update sensey-io --cert-file new-cert.pem --key-file new-key.pem

# Audit history / delete
kagi cert history sensey-io
kagi cert delete sensey-io
```

`create` requires `--name` and `--cert-file`; `--key-file` is optional and
`--path/-p` defaults to the root (`/`).

## Passwords

`kagi passwords` browses a password folder tree and reads stored credentials.
Passwords carry no name — a credential is addressed by its login **username**. A
leading-slash argument is a node path (folder segments then the username);
anything else matches by username or id.

```bash
# Browse the passwords root / a folder
kagi passwords
kagi passwords /sensey

# List every password (flat) with its folder path
kagi passwords list

# Show a password's metadata by node path, or by username/id
kagi passwords get /sensey/admin
kagi passwords get admin

# Reveal the plaintext password value
kagi passwords reveal admin
kagi passwords reveal /sensey/admin

# Audit history
kagi passwords history admin
```

The password value is **masked everywhere** — in `browse`, `list`, `get`, and
`history` output — and is only printed in the clear by `reveal`.

## Cluster issuers (workload identity)

Register a Kubernetes cluster's OIDC issuer so its workloads can authenticate to
Kagi with projected service-account tokens. These writes require an active org
and an **ADMIN/OWNER** role — log in and run `kagi org use <slug>` first.

```bash
# Register the current/selected cluster (auto-detects issuer URL + type)
kagi cluster create --context prod          # alias: kagi cluster register

# Interactive: pick a kubeconfig context; issuer URL and type auto-detected
kagi cluster create

# List registered issuers
kagi cluster list

# Update a display name / JWKS / enabled flag (issuer URL is immutable)
kagi cluster update <id|url> --name prod-cluster
kagi cluster update <id|url> --disable
kagi cluster update <id|url> --detect-jwks        # pin JWKS via kubectl
kagi cluster update <id|url> --clear-jwks         # revert to OIDC discovery

# Remove an issuer
kagi cluster delete <id|url>                # alias: kagi cluster rm
```

For a **private** cluster whose JWKS Kagi can't fetch, pass `--detect-jwks`
(read it via kubectl) or `--static-jwks-file <path>` on create. Cluster
`--type` is one of `AKS`, `EKS`, `GKE`, `OPENSHIFT`, `K3S`, `GENERIC`
(auto-detected from the issuer URL when omitted).

### Declarative apply

`kagi cluster apply` reconciles an issuer and its workload bindings from a YAML
file. It's idempotent: matching resources are updated in place, missing ones
created, unchanged ones left alone. `--prune` deletes this issuer's bindings that
are absent from the file (logged; `-y` skips the confirmation).

```bash
kagi cluster apply -f trust.yaml
kagi cluster apply -f trust.yaml --prune -y
```

```yaml
# trust.yaml
issuer:
  url: https://oidc.example/cluster   # optional — auto-detected via kubectl if omitted
  name: prod-cluster                  # display name; defaults to the issuer URL
  staticJwks: auto                    # auto (detect via kubectl) | <path to JWKS file> | omitted (public cluster)
  type: EKS                           # optional; inherits/creates as GENERIC when omitted
bindings:
  - namespace: app
    serviceAccount: api
    scopes:
      - app: /village/kaizen
        env: prod
```

## Workload bindings

Bind a `(namespace, service account)` on a registered cluster to a set of app
environments, so that workload's projected tokens can read those secrets. A
binding is keyed by `(issuer, namespace, service account)`; binding the same
triple again replaces its scopes.

```bash
# Bind a service account to one or more app environments
kagi workload create \
  --issuer <id|url> \
  --namespace app \
  --service-account api \
  --scope /village/kaizen:prod \
  --scope /village/other:staging     # alias: kagi workload bind

# Single scope via --path/--env instead of --scope
kagi workload create --issuer <id|url> --namespace app --service-account api \
  --path /village/kaizen --env prod

# List / remove bindings
kagi workload list
kagi workload delete <id>            # alias: kagi workload unbind
```

Scopes are `<app-path>:<env>`; each scope's app must belong to the active
organization.

## Tokens

Inspect and revoke Kagi access tokens. There is **no** `token create` — the CLI
never mints tokens.

```bash
kagi token list
kagi token revoke <id>       # -y to skip confirmation
```

## Scripting

Every command that prints a payload honors the global `--output/-o` flag with
`table` (default), `json`, or `yaml`:

```bash
kagi secrets list -o json
kagi cert list -o yaml
kagi org list -o json
```

`kagi pull` additionally writes to a file with `--out-file` (mode 0600), and
`kagi run` injects secrets directly into a child process — no intermediate file.

## Shell completion

```bash
kagi completion bash       > /etc/bash_completion.d/kagi
kagi completion zsh        > "${fpath[1]}/_kagi"
kagi completion fish       > ~/.config/fish/completions/kagi.fish
kagi completion powershell | Out-String | Invoke-Expression
```

Run `kagi completion <shell> --help` for the exact install steps per shell.

## Configuration

### Global flags

| Flag | Meaning | Default |
|------|---------|---------|
| `--dev` | Use local development URLs (localhost) | off |
| `-o, --output` | Output format: `table`, `json`, `yaml` | `table` |
| `--no-color` | Disable colored output | off |

`-o, --output` names the output **format**, never a file to write to. To write
`kagi pull` output to a file use `--out-file <path>`; everywhere else redirect
stdout. (Before v0.20.0 `--output` on `pull` was the file flag; passing a path
to `-o` now fails with a message pointing at `--out-file`.)

### Environment variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `KAGI_TOKEN` | Access token for non-interactive use; overrides the stored login session | unset |
| `KAGI_API_URL` | Override the API base URL | `https://api.kagi.pw` |
| `KAGI_KEYCLOAK_ISSUER` | Override the Keycloak issuer | `https://auth.kagi.pw/realms/kagi` |
| `KAGI_DISCOVERY_TIMEOUT` | OIDC discovery retry budget (Go duration, e.g. `10s`) | built-in default |

`KAGI_API_URL` / `KAGI_KEYCLOAK_ISSUER` take precedence over config-file values.
See [Non-interactive auth](#non-interactive-auth-kagi_token) for how `KAGI_TOKEN`
interacts with the stored session, the organization header, and `--personal`.

### Config files

The CLI reads `kagi.yaml` from the current directory, falling back to
`~/.kagi/config.yaml`. CWD values win. Note the filename is **`kagi.yaml`** (no
leading dot).

Recognized keys (folder model):

```yaml
api-url: https://api.kagi.pw
issuer: https://auth.kagi.pw/realms/kagi
folder-path: /village/kaizen                            # human reference only
app-id: 7a3c...                                         # stable addressing key
environment: prod
organization: sensey                                    # active org slug (display)
organization-id: 00000000-0000-0000-0000-000000000000   # active org UUID (header)
```

`organization` / `organization-id` are managed by `kagi org use` and
`kagi login`; you normally don't edit them by hand. The directory binding
(`app-id`, `folder-path`, `environment`) is written by `kagi setup`.

## Version

```bash
kagi version     # detailed build info
kagi --version   # just the version string
```

`kagi version` prints the full build information — the version, the git commit
it was built from, the build date, and the Go toolchain version. The global
`kagi --version` flag prints only the version string (`kagi version <v>`), which
`install.sh` / `install.ps1` parse.

## Go SDK

The Go SDK lives **in this repo** at [`./sdk`](./sdk) as a separate module,
`github.com/senseylabs/kagi-sdk`. The CLI consumes it locally via a `replace`
directive in `go.mod`:

```
require github.com/senseylabs/kagi-sdk v0.0.0
replace github.com/senseylabs/kagi-sdk => ./sdk
```

It is **not** currently `go get`-able as a standalone module — the tagged
`v0.0.0` is a placeholder resolved through the replace directive. To use it
today, vendor the `./sdk` directory or add your own `replace` pointing at a local
checkout.

## License

MIT
