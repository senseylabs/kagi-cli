# Kagi CLI

Command-line interface for [Kagi](https://kagi.pw) — secrets management for Sensey.

**This repository distributes the Kagi CLI. It does not contain its source.**
The source is maintained privately; every release below is built and published
from there automatically.

## Install

| Method | Platform | Command |
|---|---|---|
| Homebrew | macOS, Linux | `brew install senseylabs/tap/kagi-cli` |
| curl | macOS, Linux | `curl -sSfL https://github.com/senseylabs/kagi-cli/releases/latest/download/install.sh \| sh` |
| PowerShell | Windows | `iwr https://github.com/senseylabs/kagi-cli/releases/latest/download/install.ps1 -useb \| iex` |
| Packages | Linux | `.deb`, `.rpm` and `.apk` on the [releases page](https://github.com/senseylabs/kagi-cli/releases/latest) |
| Direct | macOS, Linux, Windows | Archives for amd64 and arm64 on the [releases page](https://github.com/senseylabs/kagi-cli/releases/latest) |

The formula is named `kagi-cli`; the binary it installs is `kagi`. Upgrade with
`brew upgrade kagi-cli`.

> The `-L` in the curl command matters. `releases/latest/download/` answers a
> redirect, and without `-L` curl pipes an empty body to `sh` and exits 0 — an
> install that reports success and does nothing.

## Verifying a download

Every release ships `kagi-cli_<version>_checksums.txt`:

```sh
VERSION=0.26.0
curl -sSfLO https://github.com/senseylabs/kagi-cli/releases/download/v${VERSION}/kagi-cli_${VERSION}_checksums.txt
curl -sSfLO https://github.com/senseylabs/kagi-cli/releases/download/v${VERSION}/kagi-cli_${VERSION}_darwin_arm64.tar.gz
sha256sum --check --ignore-missing kagi-cli_${VERSION}_checksums.txt
```

The install scripts do this for you.

## `go install` is not supported

`go install github.com/senseylabs/kagi-cli@latest` will not build a current
version: this repository carries no Go source. Use one of the methods above.

## Documentation and support

- Docs: https://docs.kagi.pw
- Bugs and questions: [open an issue](https://github.com/senseylabs/kagi-cli/issues)

## Licence

MIT — see [LICENSE](LICENSE). Applies to the released binaries and to every
previously published version of the source.
