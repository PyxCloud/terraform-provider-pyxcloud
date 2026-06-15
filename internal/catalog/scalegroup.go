package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Health-check kinds for a scale group (canonical, provider-neutral). `ec2`/`vm`
// is an instance-liveness check; `elb`/`lb` is a load-balancer health check that
// also replaces instances the LB marks unhealthy (the production ASG pattern).
const (
	HealthEC2 = "ec2" // instance-level health (AWS EC2 / GCP instance liveness)
	HealthELB = "elb" // load-balancer health (AWS ELB / GCP autohealing via HC)
)

// ScaleGroupSpec is the abstract description of a virtual-machine-scale-group —
// the canonical `virtual-machine-scale-group { min, max, desired, health }` over
// a VM spec (architecture/cpu/ram/os, reusing the VM SKU resolution), placed in
// a network (region+VPC) with a security-group across the region's AZs/zones.
// Provider-neutral.
type ScaleGroupSpec struct {
	Name         string // scale-group/component name, e.g. "web"
	Region       string // abstract pyx region_name, e.g. "Dublin"
	Provider     string // provider-facing name: aws | gcp | digitalocean
	Architecture string // x86_64 | arm64
	CPU          int    // requested vCPU (resolved to a concrete SKU)
	RAM          int    // requested RAM (GiB)
	OS           string // ubuntu | debian
	OSVersion    string // optional; defaults per defaultOSVersions

	// Autoscale bounds.
	Min     int // minimum instances (>= 0)
	Max     int // maximum instances (>= Min, >= 1)
	Desired int // desired instances (Min <= Desired <= Max); 0 defaults to Min

	// Health is the canonical health-check kind: ec2 (instance liveness) or elb
	// (load-balancer health). Empty defaults to ec2.
	Health string

	// Placement wiring (from the other components). Names are canonical and
	// resolved to provider references by the renderer. Subnets is the set of
	// canonical subnet names the group spreads across (multi-AZ); empty falls
	// back to the network's derived subnets.
	Network       string   // canonical network/place name (the VPC)
	Subnets       []string // canonical subnet names to spread across (multi-AZ)
	SecurityGroup string   // canonical security-group name to attach
}

// ScaleGroupPlan is the deterministic, catalog-resolved concrete translation of
// a ScaleGroupSpec for one provider. STRUCTURED plan (not rendered .tf) — the
// provider owns rendering and state, consistent with VMPlan / NetworkPlan (§8).
type ScaleGroupPlan struct {
	Provider     string `json:"provider"`      // aws | gcp | digitalocean
	CSP          string `json:"csp"`           // catalog token: aws | gcp | do
	RegionName   string `json:"region_name"`   // abstract pyx region
	CSPRegion    string `json:"csp_region"`    // concrete provider region (catalog-resolved)
	GroupName    string `json:"group_name"`    // logical scale-group/component name
	InstanceType string `json:"instance_type"` // concrete SKU from `virtual_machine` (e.g. t3.medium)
	Architecture string `json:"architecture"`  // resolved architecture
	CPU          int    `json:"cpu"`           // resolved vCPU
	RAM          int    `json:"ram"`           // resolved RAM (GiB)
	OSName       string `json:"os_name"`       // ubuntu | debian
	OSVersion    string `json:"os_version"`    // resolved version
	Image        string `json:"image"`         // concrete provider image (AMI / family / slug)

	Min     int    `json:"min"`     // minimum instances
	Max     int    `json:"max"`     // maximum instances
	Desired int    `json:"desired"` // desired instances
	Health  string `json:"health"`  // ec2 | elb

	// Zones are the concrete AZs/zones the group spreads across (multi-AZ),
	// derived from the region catalog. Empty for DigitalOcean.
	Zones []string `json:"zones"`

	NetworkName   string   `json:"network_name"`   // VPC/network it lives in
	SubnetNames   []string `json:"subnet_names"`   // subnets the group spreads across
	SecurityGroup string   `json:"security_group"` // SG/firewall to attach
	ResourceType  string   `json:"resource_type"`  // top provider resource, e.g. aws_autoscaling_group
}

// ErrAutoscaleUnsupported is returned when a provider has no native
// virtual-machine autoscaling primitive for the resolved region. It is a hard
// plan-time error directing the user to the supported mapping — never a silent
// fallback or an invented resource.
type ErrAutoscaleUnsupported struct {
	Provider  string
	CSP       string
	CSPRegion string
}

func (e ErrAutoscaleUnsupported) Error() string {
	// Name the provider's managed-kubernetes alternative (DOKS / LKE node-pool
	// autoscaling) so the error directs the user to the supported mapping.
	alt := "a `managed-kubernetes` component (DOKS node-pool autoscaling)"
	if strings.EqualFold(e.Provider, ProviderLinode) {
		alt = "a `managed-kubernetes` component (LKE node-pool autoscaling)"
	}
	return fmt.Sprintf(
		"virtual-machine-scale-group is not supported on provider %q (csp=%q, csp_region=%q): "+
			"this provider has no native VM autoscaling primitive (its `virtual_machine` catalog "+
			"rows are marked supports_autoscale=false), and PyxCloud does not invent a "+
			"non-existent resource. For autoscaled compute use %s, or pin a fixed-size "+
			"set of `virtual-machine` instances via `count`. "+
			"(this is a hard plan-time error, never a silent fallback)",
		e.Provider, e.CSP, e.CSPRegion, alt,
	)
}

// TranslateScaleGroup resolves a ScaleGroupSpec into a concrete ScaleGroupPlan
// using the catalog. Deterministic and catalog-driven: the csp_region comes from
// the region catalog, the instance type from the virtual_machine catalog (the
// SAME ResolveSKU path the virtual-machine component uses), the image from the OS
// catalog, and the multi-AZ zones derived deterministically from the csp_region.
// Any missing catalog data — or a provider with no autoscale primitive — surfaces
// as a hard plan-time error (never a silent fallback).
func TranslateScaleGroup(ctx context.Context, cat VMCatalog, spec ScaleGroupSpec) (ScaleGroupPlan, error) {
	if err := validateScaleGroupSpec(spec); err != nil {
		return ScaleGroupPlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return ScaleGroupPlan{}, err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))

	// DigitalOcean and Linode have no native VM autoscaling primitive — clean
	// plan-time error rather than an invented resource. This mirrors the catalog,
	// whose DO/Linode virtual_machine rows are marked supports_autoscale=false.
	if provider == ProviderDigitalOcean || provider == ProviderLinode {
		return ScaleGroupPlan{}, ErrAutoscaleUnsupported{
			Provider:  provider,
			CSP:       row.CSP,
			CSPRegion: row.CSPRegion,
		}
	}

	arch := strings.ToLower(strings.TrimSpace(spec.Architecture))
	if arch == "" {
		arch = ArchX8664
	}
	osName := strings.ToLower(strings.TrimSpace(spec.OS))
	if osName == "" {
		osName = OSUbuntu
	}
	osVersion := strings.TrimSpace(spec.OSVersion)
	if osVersion == "" {
		osVersion = defaultOSVersions[osName]
	}

	// Reuse the VM SKU resolution for the launch template's instance type.
	sku, err := cat.ResolveSKU(ctx, row.CSP, row.CSPRegion, arch, spec.CPU, spec.RAM)
	if err != nil {
		return ScaleGroupPlan{}, err
	}

	img, err := cat.ResolveImage(ctx, row.CSP, row.CSPRegion, osName, osVersion, arch)
	if err != nil {
		return ScaleGroupPlan{}, err
	}

	min, max, desired := normalizeBounds(spec.Min, spec.Max, spec.Desired)

	health := strings.ToLower(strings.TrimSpace(spec.Health))
	if health == "" {
		health = HealthEC2
	}
	health = canonicalHealth(health)

	name := spec.Name
	if name == "" {
		name = "pyxcloud-asg"
	}

	// Multi-AZ spread: derive concrete zones from the region catalog (same
	// derivation the network component uses). The group spreads across as many
	// zones as it has subnets (at least one).
	subnets := spec.Subnets
	nSubnets := len(subnets)
	if nSubnets == 0 {
		nSubnets = 1
	}
	zones := deriveZones(provider, row.CSPRegion, nSubnets)

	plan := ScaleGroupPlan{
		Provider:      provider,
		CSP:           row.CSP,
		RegionName:    row.RegionName,
		CSPRegion:     row.CSPRegion,
		GroupName:     name,
		InstanceType:  sku.Name,
		Architecture:  arch,
		CPU:           sku.CPU,
		RAM:           sku.RAM,
		OSName:        osName,
		OSVersion:     osVersion,
		Image:         img.Image,
		Min:           min,
		Max:           max,
		Desired:       desired,
		Health:        health,
		Zones:         zones,
		NetworkName:   spec.Network,
		SubnetNames:   subnets,
		SecurityGroup: spec.SecurityGroup,
	}

	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_autoscaling_group"
	case ProviderGCP:
		plan.ResourceType = "google_compute_region_instance_group_manager"
	case ProviderAzure:
		plan.ResourceType = "azurerm_linux_virtual_machine_scale_set"
	case ProviderOracle:
		plan.ResourceType = "oci_core_instance_pool"
	}
	return plan, nil
}

// normalizeBounds applies the canonical defaulting: a zero max becomes max(min,
// 1); a zero desired becomes min (clamped into [min, max]). Validation already
// guaranteed min <= max where both were set, so this only fills defaults.
func normalizeBounds(min, max, desired int) (int, int, int) {
	if max <= 0 {
		if min >= 1 {
			max = min
		} else {
			max = 1
		}
	}
	if desired <= 0 {
		desired = min
	}
	if desired < min {
		desired = min
	}
	if desired > max {
		desired = max
	}
	return min, max, desired
}

// canonicalHealth maps the accepted health aliases to the canonical token.
func canonicalHealth(h string) string {
	switch h {
	case HealthELB, "lb", "load-balancer", "loadbalancer":
		return HealthELB
	default:
		return HealthEC2
	}
}

func validateScaleGroupSpec(spec ScaleGroupSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("virtual-machine-scale-group: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("virtual-machine-scale-group: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("virtual-machine-scale-group: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if arch := strings.ToLower(strings.TrimSpace(spec.Architecture)); arch != "" && arch != ArchX8664 && arch != ArchARM64 {
		return fmt.Errorf("virtual-machine-scale-group: invalid architecture %q (x86_64 | arm64)", spec.Architecture)
	}
	if os := strings.ToLower(strings.TrimSpace(spec.OS)); os != "" && os != OSUbuntu && os != OSDebian {
		return fmt.Errorf("virtual-machine-scale-group: invalid os %q (ubuntu | debian)", spec.OS)
	}
	if spec.CPU < 1 {
		return fmt.Errorf("virtual-machine-scale-group: cpu must be >= 1, got %d", spec.CPU)
	}
	if spec.RAM < 1 {
		return fmt.Errorf("virtual-machine-scale-group: ram (GiB) must be >= 1, got %d", spec.RAM)
	}
	if spec.Min < 0 {
		return fmt.Errorf("virtual-machine-scale-group: min must be >= 0, got %d", spec.Min)
	}
	if spec.Max < 0 {
		return fmt.Errorf("virtual-machine-scale-group: max must be >= 0, got %d", spec.Max)
	}
	if spec.Desired < 0 {
		return fmt.Errorf("virtual-machine-scale-group: desired must be >= 0, got %d", spec.Desired)
	}
	// When both bounds are set, max must not be below min.
	if spec.Max > 0 && spec.Max < spec.Min {
		return fmt.Errorf("virtual-machine-scale-group: max (%d) must be >= min (%d)", spec.Max, spec.Min)
	}
	// When desired is set explicitly, it must be within [min, max] (max defaulted
	// to a positive value before this check only when both were set).
	if spec.Desired > 0 {
		if spec.Desired < spec.Min {
			return fmt.Errorf("virtual-machine-scale-group: desired (%d) must be >= min (%d)", spec.Desired, spec.Min)
		}
		if spec.Max > 0 && spec.Desired > spec.Max {
			return fmt.Errorf("virtual-machine-scale-group: desired (%d) must be <= max (%d)", spec.Desired, spec.Max)
		}
	}
	if h := strings.ToLower(strings.TrimSpace(spec.Health)); h != "" {
		switch h {
		case HealthEC2, "vm", "instance", HealthELB, "lb", "load-balancer", "loadbalancer":
		default:
			return fmt.Errorf("virtual-machine-scale-group: invalid health %q (ec2 | elb)", spec.Health)
		}
	}
	return nil
}
