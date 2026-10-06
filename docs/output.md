# Output

## Formats

`-o table | json | markdown | sarif | github`

- `markdown` is meant for pull request comments and CI job summaries; the [GitHub Action](github-action.md) builds its report from it.
- `sarif` feeds GitHub code scanning.
- `github` prints workflow annotations on the exact line of the values file: `::error` for `BREAKING` and for a target that cannot be analyzed, `::notice` for `REDUNDANT` and `FIXED`, `::warning` for the rest.
- `--details` (on `scan`) prints the per-key findings, not only the summary table.

## Writing to files

`--output-file` writes the report to a file in UTF-8, whatever the shell (PowerShell's `>` re-encodes the output of other programs). `FORMAT=FILE` writes that format and keeps the normal output on stdout, so one run can produce several reports:

```bash
deadvalues check -f values.yaml -o markdown --output-file report.md
deadvalues scan apps --output-file markdown=report.md --output-file sarif=results.sarif
```

The file is overwritten; a missing directory is an error. `prune` and `version` do not produce a report and reject the flag.

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
| 2 | Usage error, chart not found, render failed |
