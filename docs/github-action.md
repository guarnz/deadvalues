# GitHub Action

There are two actions in this repository:

| Action | What it does |
|---|---|
| `guarnz/deadvalues/setup@v0` | Installs the binary and adds it to the PATH. You write the commands |
| `guarnz/deadvalues@v0` | Runs deadvalues and writes the report to the job summary and a pull request comment. Either the built-in flow or your own command (`args`) |

Both download the release for the runner (Linux or macOS, amd64 or arm64) and verify it against `checksums.txt`. Check out with `fetch-depth: 0` so the base commit is available.

`setup` lives in a subdirectory of the same repository, like `actions/cache/restore`: both actions share the release tag and the install script, so `@v0` of one always matches `@v0` of the other. Only the root action is listed on the GitHub Marketplace.

## Setup only

```yaml
steps:
  - uses: actions/checkout@v7
    with:
      fetch-depth: 0
  - uses: guarnz/deadvalues/setup@v0
  - run: deadvalues scan apps -o sarif --output-file results.sarif --fail-on none
```

| Input | Default | Description |
|---|---|---|
| `version` | the action's ref | Release to install, e.g. `v0.1.0`, or `latest`; see [Versions](#versions) |
| `github-token` | `github.token` | Token used to download the release |

| Output | Description |
|---|---|
| `version` | The installed version |
| `path` | Path of the binary |

Nothing is written to the job summary and no comment is posted.

## Built-in flow

```yaml
on:
  pull_request:
  push:
    branches:
      - dev
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

The action compares the change that triggered the run with the state before it:

| Event | Base |
|---|---|
| `pull_request` | The pull request base: what the pull request changes |
| `push` | The commit before the push: what the push brought (every commit of it) |
| `base` input set | That ref or SHA |
| No base (first push of a branch, force push, `workflow_dispatch`, `schedule`) | None: every app is scanned, and the report says why |

What runs, by `mode`:

| `mode` | With a base | Without a base |
|---|---|---|
| `all` (default) | `diff --base` and `scan --changed-since` | `scan` of every app |
| `diff` | `diff --base` | Error: set `base` |
| `scan` | `scan --changed-since` | `scan` of every app |

`diff` catches what a chart bump does to your values (the Renovate case); `scan --changed-since` catches dead keys in the apps whose files changed.

## Your own command

```yaml
- uses: guarnz/deadvalues@v0
  with:
    args: diff --app apps/example.yaml --to 0.2.0
```

`args` replaces the built-in flow; `mode`, `base` and the `fail-on-*` inputs are ignored, so put `--fail-on` in `args` if you need it. The value is split on spaces and quotes are not supported.

## Report

The job summary and the pull request comment open with a GitHub alert, list every command that ran, then show only the commands with something to report:

```
## deadvalues

> [!CAUTION]
> Findings at the fail-on level.
>
> - `diff`: 1 breaking, 1 pinned, 1 changed, 1 fixed

Base: `0bc7546` (the commit before the push) · Docs

- :x: `deadvalues diff apps --base 0bc7546… --fail-on breaking`
- :white_check_mark: `deadvalues scan apps --changed-since 0bc7546… --details --fail-on none`

**`deadvalues diff apps --base 0bc7546… --fail-on breaking`**

### `one` — cases 0.1.0 → 0.2.0
| Change | Key | Detail | Source |
| **BREAKING** | `labels.team` | ALIVE → DEAD | values/one.yaml:10 |
```

| Alert | When |
|---|---|
| `CAUTION` | Findings at the fail-on level, an error, or nothing could run |
| `WARNING` | Findings below the fail-on level; nothing fails |
| `NOTE` | There was no base, so every app was scanned |
| none | No findings: the report says "No findings." |

Each command is marked :x: (fails the check or errored), :warning: (findings that do not fail) or :white_check_mark: (clean). The comment is posted only on pull requests, and updated on every push instead of adding a new one. On push the report is in the job summary. Deciding which commands have findings needs `jq`, which GitHub-hosted runners have; without it every command is shown.

## Versions

The binary the actions install follows the ref you use them at, so pinning the action also pins deadvalues:

| `uses:` | Installs |
|---|---|
| `guarnz/deadvalues@v0.1.0` | v0.1.0 |
| `guarnz/deadvalues@v0` | the latest v0.x release; `v0` moves with every release |
| `guarnz/deadvalues@<commit SHA>` | the latest release |

The `version` input overrides it: an exact tag such as `v0.1.0`, or `latest`. Releases in `0.x` may change flags and outputs; pin an exact version or a SHA (Renovate keeps either up to date) if your workflow must not change on its own.

## Inputs

| Input | Default | Description |
|---|---|---|
| `path` | `.` | Directory with the Applications, HelmReleases or kustomize overlays. The default scans the whole repository |
| `mode` | `all` | `all`, `diff` or `scan` |
| `base` | from the event | Ref or SHA to compare with |
| `args` | | Run this command instead of the built-in flow |
| `fail-on-diff` | `breaking` | `breaking`, `pinned`, `changed` or `none` |
| `fail-on-scan` | `none` | `dead`, `conditional`, `redundant` or `none` |
| `comment` | `true` | Post or update the pull request comment |
| `version` | the action's ref | Release to install, e.g. `v0.1.0`, or `latest`; see [Versions](#versions) |
| `github-token` | `github.token` | Token to download the release and comment |

`fail-on-scan` is `none` by default because a scan also reports keys that were already dead before the change; set it to `dead` to fail on them too.

## Outputs

| Output | Description |
|---|---|
| `report` | Path of the Markdown report |
| `status` | `0` nothing at the fail-on levels, `1` findings, `2` errors |
| `base` | The base that was compared with, empty when there was none |
| `version` | The installed version |
