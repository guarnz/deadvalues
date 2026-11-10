## deadvalues prune

Remove dead and redundant keys from a values file

### Synopsis

Remove dead and redundant keys from a values file.

Edits the file in place, removing only the lines of each key, so comments,
ordering and formatting of the remaining keys are preserved. Always shows a
diff first.

```
deadvalues prune [flags]
```

### Examples

```
  deadvalues prune --app apps/example.yaml --dry-run
  deadvalues prune -f values.yaml
  deadvalues prune --chart bitnami/nginx -f values.yaml
```

### Options

```
      --app string            Argo CD Application or Flux HelmRelease manifest
      --chart string          local chart directory or .tgz, repo/name from "helm repo add", chart name with --repo, or oci:// reference
      --dry-run               print the diff, do not write
  -h, --help                  help for prune
      --include-conditional   also remove CONDITIONAL keys
  -n, --namespace string      release namespace (default: from the manifest, or "default")
      --only string           dead | redundant | all (default "all")
      --release string        release name (default: from the manifest, or "release")
      --repo string           Helm repository URL for --chart
      --repo-root string      repository root for $values/, sources and local charts (default: git top-level)
      --set stringArray       extra values, same syntax as helm --set
  -f, --values strings        values files, merged in order; given alone, the chart is found from the Application, HelmRelease or chart directory that uses them
      --version string        chart version, semver range or OCI digest (default: latest)
  -y, --yes                   write without confirmation
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

