# Install

## Binary

Download the archive for your system from the [releases](https://github.com/guarnz/deadvalues/releases): `deadvalues_<os>_<arch>.tar.gz` for Linux and macOS, `.zip` for Windows, in `amd64` and `arm64`. It holds a single static binary; put it somewhere on your `PATH`.

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
