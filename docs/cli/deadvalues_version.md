## deadvalues version

Print version, commit and Helm SDK version

```
deadvalues version [flags]
```

### Options

```
  -h, --help   help for version
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

