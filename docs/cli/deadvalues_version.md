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
      --api-versions strings   extra API versions for .Capabilities.APIVersions.Has
      --cache-dir string       chart cache (default "<user cache dir>/deadvalues")
      --color string           auto | always | never (default "auto")
  -c, --config string          path to .deadvalues.yaml (default: repo root)
      --kube-version string    Kubernetes version for .Capabilities (default: Helm SDK default)
      --no-cache               always download charts
      --no-color               same as --color=never
  -o, --output strings         table | json | markdown | sarif | github; several, comma-separated, with --report (default [table])
      --report                 write json, markdown and sarif, or those in -o, to <command>-<date>.<ext> in the current directory
      --report-path string     write the report files here instead: a directory, a base name, or a file name when there is one file format (implies --report)
  -v, --verbose                log every render and probe
  -w, --workers int            concurrent renders (default number of CPUs)
```

### SEE ALSO

* [deadvalues](deadvalues.md)	 - Find Helm values that have no effect on the rendered chart

