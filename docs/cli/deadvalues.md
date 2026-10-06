## deadvalues

Find Helm values that have no effect on the rendered chart

### Synopsis

deadvalues finds Helm values that have no effect on the rendered chart:
typos, keys a chart renamed or removed, and keys equal to the chart default.

It renders the chart, removes each key you set and checks whether the
output changes. No template parsing, no regex.

```
deadvalues [flags]
```

### Options

```
      --api-versions strings      extra API versions for .Capabilities.APIVersions.Has
      --cache-dir string          chart cache (default "<user cache dir>/deadvalues")
      --color string              auto | always | never (default "auto")
  -c, --config string             path to .deadvalues.yaml (default: repo root)
  -h, --help                      help for deadvalues
      --kube-version string       Kubernetes version for .Capabilities (default: Helm SDK default)
      --no-cache                  always download charts
      --no-color                  same as --color=never
  -o, --output string             table | json | markdown | sarif | github (default "table")
      --output-file stringArray   write the report to a file in UTF-8 instead of stdout; FORMAT=FILE writes that format and keeps stdout (repeatable)
  -v, --verbose                   log every render and probe
      --version                   print version, commit and Helm SDK version
  -w, --workers int               concurrent renders (default number of CPUs)
```

### SEE ALSO

* [deadvalues check](deadvalues_check.md)	 - Report dead, conditional and redundant keys for one app
* [deadvalues diff](deadvalues_diff.md)	 - Compare key status between two chart versions
* [deadvalues explain](deadvalues_explain.md)	 - Show which manifest fields a key affects
* [deadvalues prune](deadvalues_prune.md)	 - Remove dead and redundant keys from a values file
* [deadvalues scan](deadvalues_scan.md)	 - Run check on every Argo CD Application and Flux HelmRelease in a directory
* [deadvalues version](deadvalues_version.md)	 - Print version, commit and Helm SDK version

