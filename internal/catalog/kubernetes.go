package catalog

import (
	"context"
	"fmt"
	"strings"
)

// ManagedKubernetes is the abstract `managed-kubernetes` component (SPEC §5.8): a
// managed control plane plus an autoscaling node pool. It maps cleanly across all
// three wave-1 providers:
//
//   - AWS: aws_eks_cluster + aws_eks_node_group (managed node group with
//     autoscaling min/max/desired), in the place's VPC subnets.
//   - GCP: google_container_cluster + google_container_node_pool (autoscaling).
//   - DigitalOcean: digitalocean_kubernetes_cluster with an auto-scaling node_pool
//     (DOKS — the DigitalOcean autoscaling answer the scale-group error points to).
//
// The node SIZE reuses the SAME `virtual_machine` SKU resolution as the VM /
// scale-group components (catalog-driven, never invented): the node pool's
// machine type is resolved from (cpu, ram, arch) via ResolveSKU.
//
// SECURITY: clusters default to a private/secure posture where the toggle exists —
// the EKS cluster endpoint stays in the VPC; node groups join the place's network.

// K8sSpec is the abstract managed-kubernetes description. Provider-neutral.
type K8sSpec struct {
	Name     string
	Region   string
	Provider string

	// Version is the Kubernetes version, e.g. "1.30"; empty -> provider default.
	Version string

	// Node pool sizing (resolved to a concrete machine type via ResolveSKU).
	Architecture string // x86_64 (default) | arm64
	NodeCPU      int
	NodeRAM      int

	// Node pool autoscaling bounds.
	MinNodes     int
	MaxNodes     int
	DesiredNodes int

	// Placement wiring.
	Network string
	Subnets []string
}

// K8sPlan is the deterministic, catalog-resolved concrete translation.
type K8sPlan struct {
	Provider   string `json:"provider"`
	CSP        string `json:"csp"`
	RegionName string `json:"region_name"`
	CSPRegion  string `json:"csp_region"`

	Name     string `json:"name"`
	Version  string `json:"version"`
	NodeType string `json:"node_type"` // concrete machine type from `virtual_machine`
	NodeCPU  int    `json:"node_cpu"`
	NodeRAM  int    `json:"node_ram"`

	MinNodes     int `json:"min_nodes"`
	MaxNodes     int `json:"max_nodes"`
	DesiredNodes int `json:"desired_nodes"`

	Zones        []string `json:"zones"`
	NetworkName  string   `json:"network_name"`
	SubnetNames  []string `json:"subnet_names"`
	ResourceType string   `json:"resource_type"`
}

// TranslateKubernetes resolves a K8sSpec into a concrete K8sPlan. Catalog-driven:
// region from the region catalog, node machine type from the `virtual_machine`
// catalog (the SAME ResolveSKU path as VM/ASG). All three providers support
// managed Kubernetes, so there is no unsupported path.
func TranslateKubernetes(ctx context.Context, cat VMCatalog, spec K8sSpec) (K8sPlan, error) {
	if err := validateK8sSpec(spec); err != nil {
		return K8sPlan{}, err
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return K8sPlan{}, err
	}
	provider := lc(spec.Provider)

	arch := lc(spec.Architecture)
	if arch == "" {
		arch = ArchX8664
	}
	sku, err := cat.ResolveSKU(ctx, row.CSP, row.CSPRegion, arch, spec.NodeCPU, spec.NodeRAM)
	if err != nil {
		return K8sPlan{}, err
	}

	min, max, desired := normalizeBounds(spec.MinNodes, spec.MaxNodes, spec.DesiredNodes)
	nSubnets := len(spec.Subnets)
	if nSubnets == 0 {
		nSubnets = 2 // a managed cluster needs >= 2 AZs for the control plane
	}

	plan := K8sPlan{
		Provider:     provider,
		CSP:          row.CSP,
		RegionName:   row.RegionName,
		CSPRegion:    row.CSPRegion,
		Name:         canonicalName(spec.Name, "pyxcloud-k8s"),
		Version:      strings.TrimSpace(spec.Version),
		NodeType:     sku.Name,
		NodeCPU:      sku.CPU,
		NodeRAM:      sku.RAM,
		MinNodes:     min,
		MaxNodes:     max,
		DesiredNodes: desired,
		Zones:        deriveZones(provider, row.CSPRegion, nSubnets),
		NetworkName:  spec.Network,
		SubnetNames:  spec.Subnets,
	}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_eks_cluster"
	case ProviderGCP:
		plan.ResourceType = "google_container_cluster"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_kubernetes_cluster"
	case ProviderAlibaba:
		plan.ResourceType = "alicloud_cs_managed_kubernetes"
	}
	return plan, nil
}

func validateK8sSpec(spec K8sSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("managed-kubernetes: region (abstract pyx region_name) is required")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("managed-kubernetes: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if arch := lc(spec.Architecture); arch != "" && arch != ArchX8664 && arch != ArchARM64 {
		return fmt.Errorf("managed-kubernetes: invalid architecture %q (x86_64 | arm64)", spec.Architecture)
	}
	if spec.NodeCPU < 1 {
		return fmt.Errorf("managed-kubernetes: node_cpu must be >= 1, got %d", spec.NodeCPU)
	}
	if spec.NodeRAM < 1 {
		return fmt.Errorf("managed-kubernetes: node_ram (GiB) must be >= 1, got %d", spec.NodeRAM)
	}
	if spec.MinNodes < 0 || spec.MaxNodes < 0 || spec.DesiredNodes < 0 {
		return fmt.Errorf("managed-kubernetes: node bounds must be >= 0")
	}
	if spec.MaxNodes > 0 && spec.MaxNodes < spec.MinNodes {
		return fmt.Errorf("managed-kubernetes: max_nodes (%d) must be >= min_nodes (%d)", spec.MaxNodes, spec.MinNodes)
	}
	return nil
}

// CanonicalKubernetesType reports whether t names the managed-kubernetes component
// (accepts the container-service alias from SPEC §3.1).
func CanonicalKubernetesType(t string) (string, bool) {
	switch lc(t) {
	case TypeManagedKubernetes, "container-service", "kubernetes", "k8s":
		return TypeManagedKubernetes, true
	}
	return "", false
}
