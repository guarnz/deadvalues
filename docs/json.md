# JSON output

`-o json`, and `--output-file json=FILE`, print one object for every command that produces a report:

```json
{
  "schemaVersion": 1,
  "deadvalues": "0.2.0",
  "command": "scan",
  "generatedAt": "2026-11-05T18:00:00Z",
  "results": []
}
```

| Field | Meaning |
|---|---|
| `schemaVersion` | Version of this format, see [Versioning](#versioning) |
| `deadvalues` | Version of the binary that wrote it |
| `command` | `check`, `scan`, `diff` or `explain` |
| `generatedAt` | When the report was written, in UTC (RFC 3339) |
| `results` | Always a list, also for one app or none |

Read the results with `jq '.results[]'`.

## Results

Each item depends on the command.

**`check` and `scan`:** one item per app.

| Field | Meaning |
|---|---|
| `app`, `appFile`, `chart`, `values` | The app, the manifest that defines it, the chart and the values files used |
| `keys` | Every key you set: `key`, `path`, `status`, `value`, `default`, `detail`, `origins` (file and line); `ignored`, `usedBy` and `sharedWith` when they apply |
| `counts` | Keys per status |
| `renders`, `durationMs`, `noisyFields` | Renders made, time taken, fields masked as noise |
| `ignored`, `usedByOtherApps`, `sharedUnverified`, `notes` | Keys left out by `ignore`, keys another app reads, shared keys not judged against every app, notes about the analysis |
| `skipped`, `skipKind` | Why the app was not analyzed: `unsupported`, `notApplied` or `unavailable` |
| `error` | Why the app could not be analyzed |

**`diff`:** one item per app whose chart version changed, with `from`, `to`, `changes` (`key`, `path`, `kind`, `from`, `to`, `value`, `oldDefault`, `newDefault`, `detail`, `origins`) and `counts` by kind. When the new chart does not render with your values, `changes` has one `BREAKING` entry with an empty `key`, the chart's message in `detail` and `template` with the `file:line` that failed, or `values.schema.json`. A `diff <dir>` where no version changed has an empty `results`.

**`explain`:** one item per app the key belongs to, with `key`, `value` and `changes` (`object`, `field`, `before`, `after`); `required` when the render breaks without the key, and `group` when it only breaks because the key cannot go alone (a schema that needs it inside its map): removing that map changes nothing, so the key is dead with it, as `scan` reports.

Keys within an app are in alphabetical order.

## Versioning

`schemaVersion` goes up only when a field is removed, renamed or changes meaning. A new field does not change it, so read the fields you know and ignore the rest.

Before v0.2.0 the output had no envelope: an object for one result and a list for several. To read both:

```bash
jq 'if type == "object" and has("schemaVersion") then .results[] elif type == "array" then .[] else . end'
```
