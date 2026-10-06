```text
 ____  _____ _____ ____  _____ _____ __    _____ _____ _____
|    \|   __|  _  |    \|  |  |  _  |  |  |  |  |   __|   __|
|  |  |   __|     |  |  |  |  |     |  |__|  |  |   __|__   |
|____/|_____|__|__|____/ \___/|__|__|_____|_____|_____|_____|
```

[![Release](https://img.shields.io/github/v/release/guarnz/deadvalues?sort=semver&label=Release)](https://github.com/guarnz/deadvalues/releases) [![Go](https://img.shields.io/github/go-mod/go-version/guarnz/deadvalues?logo=go&logoColor=white&label=Go)](https://github.com/guarnz/deadvalues/blob/main/go.mod) [![OpenSSF Scorecard](https://img.shields.io/ossf-scorecard/github.com/guarnz/deadvalues?label=OpenSSF%20Scorecard)](https://securityscorecards.dev/viewer/?uri=github.com/guarnz/deadvalues) [![GitOps](https://img.shields.io/badge/GitOps-Argo_CD_%7C_Flux-326CE5?logo=git&logoColor=white)](https://github.com/guarnz/deadvalues/blob/main/docs/argo-flux.md) [![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/deadvalues)](https://artifacthub.io/packages/search?repo=deadvalues)

**Helm values dead code detector.** Find the keys in your values files that have **no effect**, and the ones a chart upgrade silently breaks.

Helm ignores keys a chart does not read, without a warning. A chart renames `storage.data.labels` to `persistence.labels`, Renovate bumps it, and your values keep the old key: CI and Argo CD stay green while the label your backups depend on is gone. deadvalues catches that in the pull request.

![deadvalues check finds a dead key, then deadvalues diff finds the key a chart bump breaks](https://raw.githubusercontent.com/guarnz/deadvalues/main/docs/assets/demo.gif)

- **Catches:** typos, renamed or removed keys, values equal to the chart default, keys that only matter when a feature is on
- **Reads:** Argo CD Applications, Flux HelmReleases (with kustomize overlays), or a chart and values files
- **Runs as:** a CLI, a Helm plugin, a container image or a GitHub Action
- **Reports:** table, JSON, Markdown, SARIF or GitHub annotations
- **Needs:** nothing else: one static binary built on the Helm SDK, no `helm` or cluster

## Quick look

A typo, values equal to the chart default, and keys that only matter when a feature is on:

```
$ deadvalues check --app apps/example.yaml
example  webapp 0.1.0  ←  config/example/values.yaml
43 renders in 21ms, 1 random fields ignored
note: list the APIs your cluster serves under apiVersions in .deadvalues.yaml to resolve API-dependent keys

  REDUNDANT   image.repository           = chart default "nginx"
  REDUNDANT   ingress.enabled            = chart default false
  CONDITIONAL ingress.host               "app.example.com"  (only when ingress.enabled=true)
  CONDITIONAL monitoring.serviceMonitor  true  (only when the cluster serves monitoring.coreos.com/v1/ServiceMonitor)
  REDUNDANT   secretKey                  "SECRET"  (= fallback hardcoded in the template)
  DEAD        service.prot               8080  (key not in chart values.yaml)
  REDUNDANT   service.type               = chart default "ClusterIP"
  CONDITIONAL sub.size                   "large"  (only when sub.enabled=true)

  1 dead, 3 conditional, 4 redundant, 5 alive, 1 group, 1 required
```

The same values after the chart is bumped to 0.2.0 (by Renovate or by hand), which stopped reading `labels.team`:

```
$ deadvalues diff apps --base origin/main
example  webapp 0.1.0 → 0.2.0
82 renders in 49ms

  BREAKING  labels.team                ALIVE → DEAD
  PINNED    service.type               REDUNDANT → ALIVE (default "ClusterIP" → "LoadBalancer"; your value now pins the old behavior)
  CHANGED   revisionHistoryLimit       default 10 → 5 (you do not set it; the manifests change)
  FIXED     monitoring.serviceMonitor  CONDITIONAL → ALIVE

  1 breaking, 1 pinned, 1 changed, 1 fixed
```

## Install

| Method | Command |
|---|---|
| Binary | Download from the [releases](https://github.com/guarnz/deadvalues/releases) (Linux, macOS, Windows; amd64 and arm64) |
| Go 1.26+ | `go install github.com/guarnz/deadvalues/cmd/deadvalues@latest` |
| Helm plugin | `helm plugin install https://github.com/guarnz/deadvalues` (Helm 4: add `--verify=false`) |
| Container | `docker run --rm -v "$PWD:/repo" ghcr.io/guarnz/deadvalues scan apps` |

### Requirements

- **No `helm` binary and no cluster:** charts are rendered in memory with the Helm SDK.
- **`git`** for the commands that compare revisions (`diff --base/--head`, `scan --changed-since`) and to find the repository root.
- **Network access** to the chart repositories (HTTP or OCI), unless the charts are local or already cached.
- **Private repositories** use your existing Helm credentials (`helm repo add`, `helm registry login`).

Releases are signed with cosign (keyless) and ship an SBOM. How to verify them, run the container as your user and set up shell completion is in [docs/install.md](https://github.com/guarnz/deadvalues/blob/main/docs/install.md).

## Usage

Every command and flag is in the [CLI reference](https://github.com/guarnz/deadvalues/blob/main/docs/cli/deadvalues.md) (the same text as `--help`).

### `check`: one app

```bash
# From an Argo CD Application or Flux HelmRelease
deadvalues check --app apps/example.yaml

# From the values file you are editing: the chart comes from the apps that load it
deadvalues check -f config/example/values.yaml

# Against a chart you name: local, helm repo, OCI or a repository URL
deadvalues check --chart bitnami/nginx -f values.yaml
deadvalues check --chart oci://ghcr.io/org/charts/app --version 1.2.3 -f values.yaml
deadvalues check --repo https://charts.example.com --chart webapp --version 0.1.0 -f values.yaml
```

### `diff`: what a chart upgrade does to your values

```bash
# A Renovate branch: compared with the commit it branched from
deadvalues diff apps --head origin/renovate/example

# Your working tree (in CI: the pull request) against main
deadvalues diff apps --base origin/main

# Starting from a values file instead of a directory
deadvalues diff -f config/example/values.yaml --head origin/renovate/example

# One app against a chart version that is not in git yet
deadvalues diff --app apps/example.yaml --to 0.2.0
```

Only apps whose chart version changed between the two sides are diffed. The output is the one in [Quick look](#quick-look).

### `scan`: every app in a directory

```bash
# Summary table for every app
deadvalues scan apps

# Every finding, as a Markdown report
deadvalues scan apps --details -o markdown --output-file report.md

# Only the apps whose files changed in this branch
deadvalues scan apps --changed-since origin/main
```

### `explain`: what does this key change?

```
$ deadvalues explain replicas --app apps/example.yaml
example  webapp 0.2.0
replicas = 3 affects:
  Deployment/app
    .spec.replicas                                3  (without it: 1)
```

### `prune`: clean the values file

```bash
# See what would be removed
deadvalues prune --app apps/example.yaml --dry-run

# Remove only the keys equal to the chart default, from a file several apps share
deadvalues prune -f config/global-values.yaml --only redundant
```

Removes keys by deleting only their lines, so comments and formatting stay intact. Shows a diff and asks before writing (`--yes` to skip).

## Statuses

| `check` / `scan` | Meaning |
|---|---|
| `ALIVE` | The key changes the rendered manifests |
| `DEAD` | The chart ignores the key |
| `CONDITIONAL` | Only has an effect when `<x>.enabled=true`, or when the cluster serves an API |
| `REDUNDANT` | Equal to the chart default (or to a fallback in the template) |
| `GROUP` | Only has an effect together with its siblings (`if or .a .b`) |
| `ERROR` | Removing it breaks the render (`required`, schema): the key is required |
| `SHARED` | `check --app` only: the file is loaded by other apps too, so run `scan` or `check -f` on it |

| `diff` | Meaning |
|---|---|
| `BREAKING` | Worked before, has no effect now |
| `PINNED` | Was equal to the default; the default changed, so your value now pins the old behavior |
| `CHANGED` | A default you do not set changed and alters the manifests |
| `FIXED` | Was dead, works now |

Exit code `1` means findings at or above `--fail-on` (`dead` for `check`/`scan`, `breaking` for `diff`), `2` a usage or render error.

## GitHub Action

```yaml
on:
  pull_request:
  push:
    branches:
      - main
permissions:
  contents: read
  pull-requests: write
jobs:
  deadvalues:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0
      - uses: guarnz/deadvalues@v0
        with:
          path: apps
```

It compares the change with the state before it (the pull request base, or the commit before the push), diffs the chart bumps and scans the apps whose files changed. The report goes to the job summary and, on a pull request, to a single comment updated on every push. A `BREAKING` change fails the check, so Renovate does not automerge it.

```yaml
- uses: guarnz/deadvalues@v0
  with:
    args: diff --app apps/example.yaml --to 0.2.0
```

```yaml
- uses: guarnz/deadvalues/setup@v0
- run: deadvalues scan apps -o sarif --output-file results.sarif --fail-on none
```

`args` runs your own command with the same report; `setup` only installs the binary. Inputs, outputs and the report format are in [docs/github-action.md](https://github.com/guarnz/deadvalues/blob/main/docs/github-action.md).

## Documentation

- [Install](https://github.com/guarnz/deadvalues/blob/main/docs/install.md): every install method, requirements, shell completion and verifying a release
- [CLI reference](https://github.com/guarnz/deadvalues/blob/main/docs/cli/deadvalues.md): every command and flag, generated from the code
- [How it works](https://github.com/guarnz/deadvalues/blob/main/docs/how-it-works.md): the render-and-compare method, shared values files, git refs and limitations
- [Argo CD and Flux](https://github.com/guarnz/deadvalues/blob/main/docs/argo-flux.md): supported fields, kustomize overlays and `${var}` substitution
- [Configuration](https://github.com/guarnz/deadvalues/blob/main/docs/configuration.md): `.deadvalues.yaml`, ignoring keys and declaring cluster APIs
- [Output](https://github.com/guarnz/deadvalues/blob/main/docs/output.md): formats, writing reports to files, color and exit codes
- [GitHub Action](https://github.com/guarnz/deadvalues/blob/main/docs/github-action.md): the built-in flow, your own command, setup only, inputs and outputs

## Development

```bash
go test ./...
go test ./internal/cli -run Golden -update
go test ./internal/cli -bench CheckN8n -run XXX
go run ./internal/tools/gendocs
```

`go run ./internal/tools/gendocs` regenerates `docs/cli` after a change to a command or flag; CI fails if it is out of date. How deadvalues works inside is in [DESIGN.md](https://github.com/guarnz/deadvalues/blob/main/DESIGN.md), and how to contribute in [CONTRIBUTING.md](https://github.com/guarnz/deadvalues/blob/main/CONTRIBUTING.md).
