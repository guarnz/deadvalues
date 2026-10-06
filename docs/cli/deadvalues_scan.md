## deadvalues scan

Run check on every Argo CD Application and Flux HelmRelease in a directory

### Synopsis

Run check on every Argo CD Application and Flux HelmRelease in a directory.

Directories with a kustomization.yaml are built first, the way Flux does, so
HelmReleases are checked with their overlay patches applied. Sources without
a Helm chart are skipped. One failing app does not stop the scan; it is
reported as ERROR in the summary.

```
deadvalues scan <dir> [flags]
```

### Examples

```
  deadvalues scan apps
  deadvalues scan apps --changed-since origin/main
```

### Options

```
      --changed-since string   scan only targets whose files changed since this git ref
      --details                print per-key findings, not only the summary table
      --fail-on string         dead | conditional | redundant | none (default "dead")
  -h, --help                   help for scan
      --only strings           scan only these Applications or HelmReleases (by metadata.name)
      --repo-root string       repository root for $values/, sources and local charts (default: git top-level)
      --skip strings           skip these Applications or HelmReleases
```

### Options inherited from parent commands

```
      --api-versions strings      extra API versions for .Capabilities.APIVersions.Has
      --cache-dir string          chart cache (default "<user cache dir>/deadvalues")
      --color string              auto | always | never (default "auto")
  -c, --config string             path to .deadvalues.yaml (default: repo root)
      --kube-version string       Kubernetes version for .Capabilities (default: Helm SDK default)
      --no-cache                  always download charts
      --no-color                  same as --color=never
  -o, --output string             table | json | markdown | sarif | github (default "table")
      --output-file stringArray   write the report to a file in UTF-8 instead of stdout; FORMAT=FILE writes that format and keeps stdout (repeatable)
  -v, --verbose                   log every render and probe
  -w, --workers int               concurrent renders (default number of CPUs)
```

### SEE ALSO

* [deadvalues](deadvalues.md)	 - Find Helm values that have no effect on the rendered chart

