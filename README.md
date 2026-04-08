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
# Set a secret
kagi secret set --project my-app --env production --key DATABASE_URL --value "postgres://..."

# Get a secret
kagi secret get --project my-app --env production --key DATABASE_URL
```

### List projects

```bash
kagi projects
```

### List environments

```bash
kagi environments --project my-app
```

## Configuration

### Global flags

| Flag | Env var | Default |
|------|---------|---------|
| `--api-url` | `KAGI_API_URL` | `https://api.village.sensey.io` |
| `--issuer` | `KAGI_KEYCLOAK_ISSUER` | `https://auth.sensey.io/realms/sensey` |

### Config file (`.kagi.yaml`)

Place in the current directory or `~/.kagi/config.yaml`:

```yaml
api-url: https://api.village.sensey.io
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
