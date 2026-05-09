# Kagi CLI

A CLI tool for managing secrets in Kagi. Supports Keycloak Device Authorization Grant for interactive login and Personal Access Tokens (PAT) for CI/CD pipelines.

## Installation

```bash
brew tap senseylabs/tap
brew install kagi-cli
```

## Usage

### Login

```bash
kagi login
```

Opens your browser for Keycloak authentication. Stores credentials in the macOS Keychain (or `~/.kagi/credentials` on Linux).

### Setup

Interactive setup wizard to configure your project and environment:

```bash
kagi setup
```

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
| `--api-url` | `KAGI_API_URL` | `https://kagi-api.sensey.io` |
| `--issuer` | `KAGI_KEYCLOAK_ISSUER` | `https://auth.sensey.io/realms/kagi` |

### Config file (`.kagi.yaml`)

Place in the current directory or `~/.kagi/config.yaml`:

```yaml
api-url: https://kagi-api.sensey.io
project: my-project
environment: development
```

### CI/CD

Set `KAGI_TOKEN` environment variable to a Personal Access Token for non-interactive use:

```bash
export KAGI_TOKEN=vv_your_token_here
kagi pull --project my-app --env production
```

## License

MIT
