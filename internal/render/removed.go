package render

import (
	"strconv"
	"strings"

	"helm.sh/helm/v4/pkg/chart/common"
)

// removedIn maps built-in API group/versions to the Kubernetes 1.x minor
// release that stopped serving them. Charts still check some of these for
// old clusters; testing them against a newer version is pointless.
var removedIn = map[string]int{
	"extensions/v1beta1":                   22,
	"apps/v1beta1":                         16,
	"apps/v1beta2":                         16,
	"networking.k8s.io/v1beta1":            22,
	"apiextensions.k8s.io/v1beta1":         22,
	"apiregistration.k8s.io/v1beta1":       22,
	"admissionregistration.k8s.io/v1beta1": 22,
	"authentication.k8s.io/v1beta1":        22,
	"authorization.k8s.io/v1beta1":         22,
	"certificates.k8s.io/v1beta1":          22,
	"coordination.k8s.io/v1beta1":          22,
	"rbac.authorization.k8s.io/v1beta1":    22,
	"scheduling.k8s.io/v1beta1":            22,
	"batch/v1beta1":                        25,
	"discovery.k8s.io/v1beta1":             25,
	"events.k8s.io/v1beta1":                25,
	"autoscaling/v2beta1":                  25,
	"policy/v1beta1":                       25,
	"node.k8s.io/v1beta1":                  25,
	"autoscaling/v2beta2":                  26,
	"flowcontrol.apiserver.k8s.io/v1beta1": 26,
	"storage.k8s.io/v1beta1":               27,
	"flowcontrol.apiserver.k8s.io/v1beta2": 29,
	"flowcontrol.apiserver.k8s.io/v1beta3": 32,
}

// removed reports whether api ("group/version" or "group/version/Kind") is no
// longer served by Kubernetes at version kv.
func removed(api string, kv common.KubeVersion) bool {
	parts := strings.Split(api, "/")
	if len(parts) < 2 {
		return false
	}
	since, ok := removedIn[parts[0]+"/"+parts[1]]
	if !ok || kv.Major != "1" {
		return false
	}
	minor, err := strconv.Atoi(strings.TrimRight(kv.Minor, "+"))
	return err == nil && minor >= since
}
