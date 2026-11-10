# Output

## Formats

`-o table | json | markdown | sarif | github`

- `json` is one object with a versioned envelope and the results in a list, for scripts; the fields and the versioning rules are in [JSON output](json.md).
- `markdown` is meant for pull request comments and CI job summaries; the [GitHub Action](github-action.md) builds its report from it.
- `sarif` feeds GitHub code scanning.
- `github` prints workflow annotations on the exact line of the values file: `::error` for `BREAKING` and for a target that cannot be analyzed, `::notice` for `REDUNDANT`, `FIXED` and a target listed as not analyzed, `::warning` for the rest.
- What deadvalues finds and does not analyze is listed with the reason and counted in the summary line: `not supported yet` for a source it does not read (`ApplicationSet`, `ResourceSet` releases, helmfile), `not applied` for a HelmRelease no Flux `Kustomization` applies, and `not analyzed` for the rest, such as a Helm chart in a git repository that could not be fetched. In `json`, `skipKind` says which: `unsupported`, `notApplied` or `unavailable`. It is not an error and does not change the exit code; in `json` the reason is in `skipped`.
- `--details` (on `scan`) prints the per-key findings, not only the summary table.
- In `table` and `markdown`, keys are listed by severity (`DEAD`, `SHARED`, `CONDITIONAL`, `REDUNDANT`, then the effective ones), alphabetically within each status; `diff` lists `BREAKING` first. `json` keeps every key in alphabetical order.

## Writing to files

`--report` writes the report to files in UTF-8, whatever the shell (PowerShell's `>` re-encodes the output of other programs), and keeps the table on screen. The files are named `<command>-<date>.<ext>` in the current directory: `scan-2026-11-10.json`, `.md` and `.sarif`. Without a file format in `-o` it writes all three (`explain`: json and Markdown); with `-o`, only those listed, and `-o` takes several formats, comma-separated:

```bash
deadvalues scan apps --report                                 # table on screen, scan-<date>.json, .md and .sarif
deadvalues scan apps -o table,sarif --report                  # table on screen, scan-<date>.sarif
deadvalues scan apps -o markdown,sarif --report-path reports/ # reports/scan-<date>.md and .sarif
deadvalues scan apps --report-path reports/homelab            # reports/homelab.json, .md and .sarif
deadvalues scan apps --report-path results.sarif              # that file only
```

`--report-path` implies `--report` and says where: a directory (an existing one, or a path ending in `/`) gets the default names; a file name with the extension of a format (`.json`, `.md`, `.sarif`) is that one file; anything else is a base name, to which each format adds its extension. Missing directories are created and the files are overwritten, so a second run on the same day replaces the first; the time is in the report (`generatedAt` in json). Each file written is printed on stderr. `table` or `github`, when listed in `-o`, stays on screen. Without `--report`, `-o` takes one format and prints it on stdout, so `-o json | jq` works as before. `prune` and `version` do not produce a report and reject these flags.

## Color

Only the `table` output has color.

| Situation | Color |
|---|---|
| `--color auto` (default) and output to a terminal | on |
| `--color auto` and output redirected (pipe, file, CI) | off |
| `--color always` | on, even without a terminal (useful in CI logs) |
| `--color never` or `--no-color` | off |
| `NO_COLOR` set | off, unless `--color always` |
| `FORCE_COLOR` set | on in `auto` |
| `TERM=dumb` | off in `auto` |

`deadvalues`, `deadvalues --help` and `deadvalues version` print the banner only on a terminal; outside one, `version` prints plain lines that scripts can read.

## Exit codes

| Exit code | Meaning |
|---|---|
| 0 | Nothing at or above `--fail-on` |
| 1 | Findings at or above `--fail-on` (`check`/`scan`: `dead` by default; `diff`: `breaking`) |
| 2 | Usage error, chart not found, render failed (a target listed as not analyzed does not count) |

With `--fail-on-error=false`, or `failOnError: false` in [`.deadvalues.yaml`](configuration.md#fail-on), an app that cannot be analyzed is still reported but does not exit 2: the code is 1 or 0 from the findings, as in CI where a registry outage should not block a pull request that a breaking change would. A usage error, or a directory or ref that does not exist, still exits 2. `--fail-on false` is the same as `--fail-on none`.
