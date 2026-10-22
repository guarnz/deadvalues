# Install

## Binary

Download the latest release for your system:

- [Linux amd64](https://github.com/guarnz/deadvalues/releases/latest/download/deadvalues_linux_amd64.tar.gz) (Intel or AMD)
- [Linux arm64](https://github.com/guarnz/deadvalues/releases/latest/download/deadvalues_linux_arm64.tar.gz) (Raspberry Pi, Graviton, Ampere)
- [macOS arm64](https://github.com/guarnz/deadvalues/releases/latest/download/deadvalues_darwin_arm64.tar.gz) (Apple silicon, M1 and later)
- [macOS amd64](https://github.com/guarnz/deadvalues/releases/latest/download/deadvalues_darwin_amd64.tar.gz) (Intel)
- [Windows amd64](https://github.com/guarnz/deadvalues/releases/latest/download/deadvalues_windows_amd64.zip) (Intel or AMD)
- [Windows arm64](https://github.com/guarnz/deadvalues/releases/latest/download/deadvalues_windows_arm64.zip) (Snapdragon)

Not sure which one? Run `uname -m` on Linux or macOS (`x86_64` is amd64, `aarch64` or `arm64` is arm64), or `$env:PROCESSOR_ARCHITECTURE` in PowerShell (`AMD64` or `ARM64`). Older versions are on the [releases](https://github.com/guarnz/deadvalues/releases) page.

Each archive holds a single static binary; put it somewhere on your `PATH`.

On macOS, a binary downloaded with a browser is quarantined by Gatekeeper. Remove the flag before the first run:

```bash
xattr -d com.apple.quarantine deadvalues
```

## Go

```bash
go install github.com/guarnz/deadvalues/cmd/deadvalues@latest
```

Needs Go 1.26 or later. `deadvalues version` shows the installed tag.

## Helm plugin

```bash
helm plugin install https://github.com/guarnz/deadvalues
helm deadvalues check --chart ./mychart -f values.yaml
```

Helm 4 verifies plugin signatures by default, which a plugin installed from a git repository cannot provide; add `--verify=false`:

```bash
helm plugin install https://github.com/guarnz/deadvalues --verify=false
```

`--verify=false` skips the check of the plugin package (`plugin.yaml` and the install script), not of deadvalues itself: the install hook only installs a binary that matches the release `checksums.txt`, which is signed with cosign. For a fully verified install with nothing to set up first, use the binary or the container image and [verify the release](#verifying-a-release).

The install hook downloads the release binary for your system, checks it against `checksums.txt` and keeps it in the plugin directory. It needs `curl`, and `tar` (or `unzip` on Windows). Add `--version v0.1.0` to install a specific release.

## Container

```bash
docker run --rm -v "$PWD:/repo" ghcr.io/guarnz/deadvalues scan apps
```

- The working directory is `/repo`; mount the repository root, `.git` included, so `diff --base` and `scan --changed-since` can read the history.
- The image runs as a non-root user (UID 65532). `prune` writes to the mounted files, so run it as your user: `docker run --rm -u "$(id -u):$(id -g)" -v "$PWD:/repo" ghcr.io/guarnz/deadvalues prune ...`.
- For private chart repositories, mount your Helm configuration: `-e HELM_CONFIG_HOME=/helm -v "$HOME/.config/helm:/helm:ro"` (on macOS it is `~/Library/Preferences/helm`).

## pre-commit

```yaml
repos:
  - repo: https://github.com/guarnz/deadvalues
    rev: v0.1.8
    hooks:
      - id: deadvalues
```

On every commit that changes a YAML file, the hook runs `deadvalues scan . --changed-since HEAD`: only the apps the staged changes touch are analyzed, including the apps that load a changed values file, and a dead key stops the commit. Changes that are not staged are left out.

- **Folder and level:** `args` replaces the defaults, so keep the folder in it: `args: [kubernetes, --fail-on, redundant]`. Without `args`, the folder is the repository root and the level comes from `failOn` in `.deadvalues.yaml`, or `dead`.
- **Only report:** `args: [., --fail-on, none]` with `verbose: true`, since pre-commit hides the output of hooks that pass.
- **Apps that cannot render outside the cluster:** `--skip` in `args`, as with `scan`. An error stops the commit even with `--fail-on none`.
- **First run:** pre-commit builds deadvalues at the `rev` version, downloading Go if needed. It takes a couple of minutes and about 430 MB in its cache (`~/.cache/pre-commit`); later commits take seconds. Charts are downloaded once and kept in the deadvalues cache.
- **Binary already installed:** use `id: deadvalues-system` to run the `deadvalues` in your `PATH` instead of building it.
- After changing `.pre-commit-config.yaml`, `git add` it: pre-commit does not run with an unstaged configuration.

With another hook manager, such as lefthook, run the same command on commit: `deadvalues scan . --changed-since HEAD`.

## Requirements

- **No `helm` binary and no cluster:** charts are rendered in memory with the Helm SDK.
- **`git`** for the commands that compare revisions (`diff --base/--head`, `scan --changed-since`) and to find the repository root. Without git, pass `--repo-root`.
- **Network access** to the chart repositories (HTTP or OCI), unless the charts are local or already cached.
- **Private repositories** use your Helm credentials: `helm repo add` (`repositories.yaml`) and `helm registry login`.

## Shell completion

```bash
deadvalues completion bash > /etc/bash_completion.d/deadvalues
deadvalues completion zsh > "${fpath[1]}/_deadvalues"
deadvalues completion fish > ~/.config/fish/completions/deadvalues.fish
deadvalues completion powershell | Out-String | Invoke-Expression
```

## Verifying a release

Every release ships `checksums.txt`, signed with cosign (keyless, from the release workflow), and an SBOM for each archive. To check a download:

```bash
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/guarnz/deadvalues/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

The first command proves `checksums.txt` was produced by this repository's release workflow; the second, that your archive matches it.

The image is signed the same way:

```bash
cosign verify ghcr.io/guarnz/deadvalues:<version> \
  --certificate-identity-regexp '^https://github.com/guarnz/deadvalues/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
