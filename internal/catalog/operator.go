package catalog

import (
	"fmt"
	"strings"
)

// operator.go — the REUSABLE operator-pattern rendering convention.
//
// THE convention for self-hosted-on-Kubernetes replacements of AWS managed
// services (Tempo for X-Ray, cert-manager for ACM, and the pending Prometheus/
// Vault replacements). It follows the Kubernetes OPERATOR pattern, split in two:
//
//   - CORE  (maintained upstream): the operator's controller + CRDs, installed via
//     the project's official Helm chart. Rendered as a `helm_release`. We do NOT
//     hand-roll the controller Deployment — the upstream chart owns the controller,
//     RBAC, CRDs, and version lifecycle.
//   - EXTRA (maintained by us): our custom resources (CRs) + their config, the
//     thing the operator reconciles (e.g. an OpenTelemetryCollector, a TempoStack,
//     a ClusterIssuer). Rendered as `kubernetes_manifest` of the operator's CRDs.
//
// Both halves reuse the same in-cluster wiring convention the rest of the DOKS
// paths use: a `data "digitalocean_kubernetes_cluster"` reference so the helm +
// kubernetes manifests land on the right cluster's kube credentials.
//
// A `helm_release` needs the `hashicorp/helm` provider, so any component that
// renders one must flip the `needsHelm` pin in assemble.go — mirroring the
// existing `needsKubernetes` pin for `kubernetes_manifest`.

// HelmReleaseSpec describes the CORE half of an operator install: a single
// `helm_release` of the upstream operator chart. Deterministic — chart, repo and
// version are pinned so the rendered plan never drifts.
type HelmReleaseSpec struct {
	// TFName is the terraform resource local name (already tf-safe), e.g.
	// "app-traces_otel_operator".
	TFName string
	// ReleaseName is the Helm release name installed into the cluster.
	ReleaseName string
	// Repository is the chart repository URL (e.g. https://open-telemetry.github.io/opentelemetry-helm-charts).
	Repository string
	// Chart is the chart name within the repository (e.g. opentelemetry-operator).
	Chart string
	// Version pins the chart version for a deterministic plan.
	Version string
	// Namespace is the namespace the operator is installed into.
	Namespace string
	// CreateNamespace asks Helm to create the namespace if absent.
	CreateNamespace bool
	// Set are optional `set { name/value }` chart values, applied in order.
	Set []HelmSet
	// ClusterDataRef is the terraform local name of the
	// `data "digitalocean_kubernetes_cluster"` block this release depends on, so
	// the release waits for the cluster reference. Empty -> no depends_on.
	ClusterDataRef string
}

// HelmSet is one chart value override (`set { name = ..., value = ... }`).
type HelmSet struct {
	Name  string
	Value string
}

// ManifestCR describes the EXTRA half: one custom resource the operator
// reconciles, rendered as a `kubernetes_manifest`. The Manifest is the already
// HCL-formatted `manifest = { ... }` body WITHOUT the surrounding resource block
// (renderOperatorCR wraps it).
type ManifestCR struct {
	// TFName is the terraform resource local name (already tf-safe).
	TFName string
	// Manifest is the HCL object literal for the `manifest = {…}` attribute,
	// already indented to sit inside the resource block (no trailing newline
	// required). Render it with renderManifestBody / hand-built strings.Builder.
	Manifest string
	// DependsOn are terraform references (e.g. "helm_release.app_otel_operator")
	// this CR must wait for — at minimum the CORE operator that owns its CRD.
	DependsOn []string
}

// renderClusterDataSource renders the shared DOKS cluster data source both the
// CORE helm_release and the EXTRA CRs reference. tfLocal is the resource local
// name; clusterName is the existing DOKS cluster name.
func renderClusterDataSource(tfLocal, clusterName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "data \"digitalocean_kubernetes_cluster\" %q {\n", tfLocal)
	fmt.Fprintf(&b, "  name = %q\n", clusterName)
	b.WriteString("}\n\n")
	return b.String()
}

// renderHelmRelease renders the CORE half — an upstream operator chart as a
// `helm_release`. This is the convention every operator-pattern component uses to
// install its controller + CRDs; nobody hand-rolls the controller Deployment.
func renderHelmRelease(h HelmReleaseSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"helm_release\" %q {\n", h.TFName)
	fmt.Fprintf(&b, "  name       = %q\n", h.ReleaseName)
	fmt.Fprintf(&b, "  repository = %q\n", h.Repository)
	fmt.Fprintf(&b, "  chart      = %q\n", h.Chart)
	fmt.Fprintf(&b, "  version    = %q\n", h.Version)
	fmt.Fprintf(&b, "  namespace  = %q\n", h.Namespace)
	if h.CreateNamespace {
		b.WriteString("  create_namespace = true\n")
	}
	// Chart value overrides. The helm provider v3 models `set` as a list attribute
	// of objects (`set = [{ name, value }]`), which is also accepted by v2 — so the
	// attribute form is the forward- and backward-compatible rendering.
	if len(h.Set) > 0 {
		b.WriteString("  set = [\n")
		for _, s := range h.Set {
			fmt.Fprintf(&b, "    { name = %q, value = %q },\n", s.Name, s.Value)
		}
		b.WriteString("  ]\n")
	}
	if h.ClusterDataRef != "" {
		fmt.Fprintf(&b, "  depends_on = [data.digitalocean_kubernetes_cluster.%s]\n", h.ClusterDataRef)
	}
	b.WriteString("}\n\n")
	return b.String()
}

// renderOperatorCR renders one EXTRA custom resource as a `kubernetes_manifest`,
// wiring its depends_on (at minimum the CORE operator that owns the CRD).
func renderOperatorCR(cr ManifestCR) string {
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"kubernetes_manifest\" %q {\n", cr.TFName)
	b.WriteString(cr.Manifest)
	if !strings.HasSuffix(cr.Manifest, "\n") {
		b.WriteString("\n")
	}
	if len(cr.DependsOn) > 0 {
		fmt.Fprintf(&b, "  depends_on = [%s]\n", strings.Join(cr.DependsOn, ", "))
	}
	b.WriteString("}\n\n")
	return b.String()
}

// renderOperatorComponent renders a full operator-pattern component: the shared
// DOKS cluster data source, the CORE upstream-operator helm_release(s), then the
// EXTRA custom resources — in dependency order. This is the single entry point
// self-hosted-on-k8s replacements should call so the CORE/EXTRA split is uniform.
func renderOperatorComponent(clusterDataLocal, clusterName string, core []HelmReleaseSpec, extra []ManifestCR) string {
	var b strings.Builder
	b.WriteString(renderClusterDataSource(clusterDataLocal, clusterName))
	for _, h := range core {
		b.WriteString(renderHelmRelease(h))
	}
	for _, cr := range extra {
		b.WriteString(renderOperatorCR(cr))
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
