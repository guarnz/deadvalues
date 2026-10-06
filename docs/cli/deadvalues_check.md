## deadvalues check

Report dead, conditional and redundant keys for one app

### Synopsis

Report dead, conditional and redundant keys for one app.

The target is a GitOps manifest (--app: an Argo CD Application or a Flux
HelmRelease), a chart plus values files (--chart and -f), or just values
files (-f): their chart is then found from the app that uses them.

```
deadvalues check [flags]
```

### Examples

```
  deadvalues check --app apps/example.yaml
  deadvalues check -f values.yaml
  deadvalues check --chart bitnami/nginx -f values.yaml
```

### Options

```
      --alive              also list keys that have an effect
      --app string         Argo CD Application or Flux HelmRelease manifest
      --chart string       local chart directory or .tgz, repo/name from "helm repo add", chart name with --repo, or oci:// reference
      --fail-on string     dead | conditional | redundant | none (default "dead")
  -h, --help               help for check
  -n, --namespace string   release namespace (default: from the manifest, or "default")
      --release string     release name (default: from the manifest, or "release")
      --repo string        Helm repository URL for --chart
      --repo-root string   repository root for $values/, sources and local charts (default: git top-level)
      --set stringArray    extra values, same syntax as helm --set
  -f, --values strings     values files, merged in order; given alone, the chart is found from the Application, HelmRelease or chart directory that uses them
      --version string     chart version or semver range (default: latest)
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

