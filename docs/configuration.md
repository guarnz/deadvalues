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

`kubeVersion` sets `.Capabilities.KubeVersion` for charts that compare it (`semverCompare`); without it, the Helm SDK default is used. `--kube-version` wins over it.

## API versions

Charts often render resources only when the cluster serves an API (`.Capabilities.APIVersions.Has "monitoring.coreos.com/v1/ServiceMonitor"`). Offline, those keys are reported as `CONDITIONAL`, naming the API they wait for (`only when the cluster serves monitoring.coreos.com/v1/ServiceMonitor`). List the ones your cluster serves in `apiVersions`, with the exact string the chart checks, and they are judged as on the cluster.

## Ignore

In `ignore`, `*` matches one key segment, `**` any number of segments, and a pattern matching a map covers everything under it. `app` is the Application's or HelmRelease's `metadata.name`, without the namespace; without `app`, the pattern applies to every app.

## Fail on

`failOn` sets the level that makes a command exit with code 1. A command uses it only when the level is valid for it: `dead`, `conditional` or `redundant` for `check` and `scan`, `breaking`, `pinned` or `changed` for `diff`, `none` for all. `--fail-on` wins over it.

## Flux

`flux.substitute` gives values to `${var}` placeholders. See [Argo CD and Flux](argo-flux.md#flux-variables).
