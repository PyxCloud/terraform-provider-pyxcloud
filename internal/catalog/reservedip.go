package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Reserved IP is the abstract `reserved-ip` component — the net-new
// DigitalOcean migration target that replaces the VPN's stable static endpoint
// (AWS aws_eip) so the WireGuard/VPN box keeps a fixed, re-attachable public IP
// across instance replacement (board task pd-MIG-RESERVED-IP, epic
// EPIC-AWS-TO-DO-MIGRATION). Like object-storage / container-registry it has NO
// sizing catalog: a reserved IP is region-scoped and billed flat, so the only
// catalog lookup is the region (region_name + provider -> csp_region). It
// depends on the RegionCatalog only.
//
// WHY THIS MATTERS (memory: mcp-sso-502-asg-resilience): the VPN box self-heals
// via an ASG-of-1; a stable, re-attachable elevated IP is exactly what lets the
// replacement instance reclaim the same public endpoint so peers' WireGuard
// configs (Endpoint = <stable-ip>:51820) keep working without re-distribution.
//
// SCOPE (SPEC §5 ethos, maps cleanly across providers):
//   - AWS:          aws_eip                     (the EIP being migrated FROM)
//   - GCP:          google_compute_address      (a regional static external IP)
//   - DigitalOcean: digitalocean_reserved_ip    (the migration target)

// Canonical reserved-ip type token. `reserved-ip` is canonical; `static-ip` and
// `elastic-ip` are accepted aliases (all name the same component).
const (
	TypeReservedIP = "reserved-ip"
	TypeStaticIP   = "static-ip"
	TypeElasticIP  = "elastic-ip"
)

// ReservedIPSpec is the abstract description of a reserved/static public IP — the
// canonical `reserved-ip { name, attach_to }`, placed in the place's region.
// Provider-neutral.
type ReservedIPSpec struct {
	Name     string // reserved-ip/component name, e.g. "vpn-endpoint"
	Region   string // abstract pyx region_name, e.g. "Frankfurt"
	Provider string // provider-facing name: aws | gcp | digitalocean

	// AttachTo is the canonical name of the compute target (a virtual-machine
	// component) the IP should be bound to. Optional: an unattached reserved IP is
	// valid (it reserves the address; binding happens when the instance exists).
	// When set, the renderer wires the provider-specific association.
	AttachTo string
}

// ReservedIPPlan is the deterministic, catalog-resolved concrete translation of a
// ReservedIPSpec for one provider. STRUCTURED plan (not rendered .tf) — the
// provider owns rendering and state, consistent with the other components (§8).
type ReservedIPPlan struct {
	Provider   string `json:"provider"`    // aws | gcp | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | gcp | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)

	LogicalName string `json:"logical_name"` // the user's abstract name (tf resource label)
	AttachTo    string `json:"attach_to"`    // canonical compute target name ("" = unattached)

	ResourceType string `json:"resource_type"` // top provider resource, e.g. digitalocean_reserved_ip
}

// ReservedIPCatalog is the resolution boundary for reserved IPs. Only region
// resolution is needed (no sizing table), so RegionCatalog suffices.
type ReservedIPCatalog = RegionCatalog

// TranslateReservedIP resolves a ReservedIPSpec into a concrete ReservedIPPlan
// using the catalog. Deterministic and catalog-driven: the csp_region comes from
// the region catalog (never invented), and any missing catalog data surfaces as a
// hard plan-time error (never a silent fallback), per SPEC §4.
func TranslateReservedIP(ctx context.Context, cat ReservedIPCatalog, spec ReservedIPSpec) (ReservedIPPlan, error) {
	if err := validateReservedIPSpec(spec); err != nil {
		return ReservedIPPlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return ReservedIPPlan{}, err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))

	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = "pyxcloud-reserved-ip"
	}

	plan := ReservedIPPlan{
		Provider:    provider,
		CSP:         row.CSP,
		RegionName:  row.RegionName,
		CSPRegion:   row.CSPRegion,
		LogicalName: name,
		AttachTo:    strings.TrimSpace(spec.AttachTo),
	}

	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_eip"
	case ProviderGCP:
		plan.ResourceType = "google_compute_address"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_reserved_ip"
	default:
		// reserved-ip is a wave-1 (aws/gcp/do) component; other providers are
		// rejected at render time with a clear message (no silent fallback).
	}
	return plan, nil
}

func validateReservedIPSpec(spec ReservedIPSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("reserved-ip: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("reserved-ip: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("reserved-ip: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	return nil
}

// CanonicalReservedIPType maps an accepted type token (reserved-ip / static-ip /
// elastic-ip) to the canonical reserved-ip token, reporting whether it is a
// recognised type.
func CanonicalReservedIPType(t string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case TypeReservedIP, TypeStaticIP, TypeElasticIP:
		return TypeReservedIP, true
	default:
		return "", false
	}
}
