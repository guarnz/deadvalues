## deadvalues diff

Compare key status between two chart versions

### Synopsis

Compare key status between two chart versions.

Runs check against the old and the new chart version with the same values
and reports what changed: keys that stopped working (BREAKING), keys whose
default changed under them (PINNED), defaults you do not set that now alter
the manifests (CHANGED) and dead keys that came back (FIXED).

With a directory, every Argo CD Application and Flux HelmRelease in it is
compared between two git revisions, and only the ones whose chart version
changed are diffed: --base is the "before" side, --head the "after" side.
Without --head the "after" side is your working tree; without --base it is
the commit --head branched from, as in a pull request. With -f alone, the
same is done for the Applications and HelmReleases that load those files.

```
deadvalues diff [dir] [flags]
```

### Examples

```
  deadvalues diff apps --head origin/renovate/example
  deadvalues diff -f config/example/values.yaml --head origin/renovate/example
  deadvalues diff apps --base origin/main
  deadvalues diff --app apps/example.yaml --to 0.2.0
```

### Options

```
      --app string         Argo CD Application or Flux HelmRelease manifest
      --base string        git ref of the "before" side (default: where --head branched from)
      --chart string       local chart directory or .tgz, repo/name from "helm repo add", chart name with --repo, or oci:// reference
      --fail-on string     breaking | pinned | changed | none (default "breaking")
      --from string        old chart version (default: version in the manifest)
      --head string        git ref of the "after" side (default: your working tree)
  -h, --help               help for diff
  -n, --namespace string   release namespace (default: from the manifest, or "default")
      --release string     release name (default: from the manifest, or "release")
      --repo string        Helm repository URL for --chart
      --repo-root string   repository root for $values/, sources and local charts (default: git top-level)
      --set stringArray    extra values, same syntax as helm --set
      --to string          new chart version
  -f, --values strings     values files, merged in order; given alone, the chart is found from the Application, HelmRelease or chart directory that uses them
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

