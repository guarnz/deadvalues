# Configuration

Optional `.deadvalues.yaml` at the repository root:

```yaml
kubeVersion: "1.36"
apiVersions:
  - monitoring.coreos.com/v1/ServiceMonitor
  - monitoring.coreos.com/v1/PrometheusRule
failOn: dead
ignore:
  - app: example
    keys:
      - strategy.rollingUpdate
  - keys:
      - "**.podAnnotations"
flux:
  substitute:
    cluster_domain: example.com
```

`-c, --config` points to another file. Unknown fields are an error, so a typo in the config does not go unnoticed.

## Kubernetes version

`kubeVersion` sets `.Capabilities.KubeVersion` for charts that compare it (`semverCompare`); without it, the Helm SDK default is used. An Argo CD Application's `helm.kubeVersion` wins over it for that app, as in Argo CD; `--kube-version` wins over both.

## API versions

Charts often render resources only when the cluster serves an API (`.Capabilities.APIVersions.Has "monitoring.coreos.com/v1/ServiceMonitor"`, or `has "monitoring.coreos.com/v1/ServiceMonitor" .Capabilities.APIVersions`). Offline, those keys are reported as `CONDITIONAL`, naming the API they wait for (`only when the cluster serves monitoring.coreos.com/v1/ServiceMonitor`). List the ones your cluster serves in `apiVersions`, with the exact string the chart checks, and they are judged as on the cluster. As in a cluster's discovery, an entry with a kind (`monitoring.coreos.com/v1/ServiceMonitor`) also serves its group/version (`monitoring.coreos.com/v1`), so one entry covers charts that check either form; `--api-versions` works the same way. An Argo CD Application's `helm.apiVersions` replaces `apiVersions` for that app, as Argo CD replaces the cluster's; `--api-versions` is still added on top.

`scan` and `diff <dir>` find the APIs your repository installs on their own, so you only list what is installed outside it. When a chart checks an API nothing declares, they look for its CRD among what the same cluster applies: plain manifests, the `crds/` directory of the other charts and their subcharts, and templates, rendered with the app's values so a flag like `crds.enabled: false` counts. Clusters are told apart by the [Flux roots](argo-flux.md#clusters) and by the `destination` of Argo CD Applications, so a CRD of one cluster does not count for another. Apps left out with `--skip`, `--only` or a narrower directory still count: they are out of the report, not out of the cluster. A key that only matters while the CRD is missing, such as cilium's `trustCRDsExist` on a first install, stays `ALIVE`. A line on stderr lists the APIs taken from the repository and what installs them. `check` and `explain` look at one app, so they only use `apiVersions`. A CRD from a chart in another git repository, a remote kustomize resource or anything applied by hand goes in `apiVersions`.

## Ignore

In `ignore`, `*` matches one key segment, `**` any number of segments, and a pattern matching a map covers everything under it. `app` is the Application's or HelmRelease's `metadata.name`, without the namespace; without `app`, the pattern applies to every app.

## Fail on

`failOn` sets the level that makes a command exit with code 1. A command uses it only when the level is valid for it: `dead`, `conditional` or `redundant` for `check` and `scan`, `breaking`, `pinned` or `changed` for `diff`, `none` for all. `--fail-on` wins over it.

## Flux

`flux.substitute` gives values to `${var}` placeholders. See [Argo CD and Flux](argo-flux.md#flux-variables).
