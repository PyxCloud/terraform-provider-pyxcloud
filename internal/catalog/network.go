package catalog

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// NetworkSpec is the abstract description of a place's network — the canonical
// `place { region = ...; cidr = ...; subnets = [...] }`. It is provider-neutral.
type NetworkSpec struct {
	Name     string   // place / network name, e.g. "production"
	Region   string   // abstract pyx region_name, e.g. "Dublin"
	Provider string   // provider-facing name: aws | gcp | digitalocean
	CIDR     string   // VPC CIDR, e.g. "10.0.0.0/16"
	Subnets  []string // subnet CIDRs; must be inside CIDR
}

// SubnetPlan is one concrete subnet in the translated plan.
type SubnetPlan struct {
	Name string `json:"name"`
	CIDR string `json:"cidr"`
	// Zone is the concrete provider availability zone (AWS) / zone (GCP).
	// Empty for DigitalOcean, whose VPC is region-scoped with no sub-zones.
	Zone string `json:"zone,omitempty"`
}

// NetworkPlan is the deterministic, catalog-resolved concrete translation of a
// NetworkSpec for one provider. It is a STRUCTURED plan (not rendered .tf) — the
// provider owns rendering and state (§8 open question resolved this way).
type NetworkPlan struct {
	Provider     string       `json:"provider"`      // aws | gcp | digitalocean
	CSP          string       `json:"csp"`           // catalog token: aws | gcp | do
	RegionName   string       `json:"region_name"`   // abstract pyx region
	CSPRegion    string       `json:"csp_region"`    // concrete provider region
	VPCName      string       `json:"vpc_name"`      // logical VPC/network name
	CIDR         string       `json:"cidr"`          // VPC CIDR
	Subnets      []SubnetPlan `json:"subnets"`       // concrete subnets (with zones where applicable)
	ResourceType string       `json:"resource_type"` // top provider resource, e.g. aws_vpc
}

// TranslateNetwork resolves a NetworkSpec into a concrete NetworkPlan using the
// catalog. Deterministic and catalog-driven: the csp_region comes from the
// catalog, zones are derived deterministically from the csp_region, and any
// missing catalog data surfaces as a hard error (never a silent fallback).
func TranslateNetwork(ctx context.Context, cat RegionCatalog, spec NetworkSpec) (NetworkPlan, error) {
	if err := validateSpec(spec); err != nil {
		return NetworkPlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return NetworkPlan{}, err
	}

	name := spec.Name
	if name == "" {
		name = "pyxcloud"
	}

	plan := NetworkPlan{
		Provider:   strings.ToLower(spec.Provider),
		CSP:        row.CSP,
		RegionName: row.RegionName,
		CSPRegion:  row.CSPRegion,
		VPCName:    name,
		CIDR:       spec.CIDR,
	}

	zones := deriveZones(plan.Provider, row.CSPRegion, len(spec.Subnets))

	subnets := make([]SubnetPlan, 0, len(spec.Subnets))
	for i, cidr := range spec.Subnets {
		sp := SubnetPlan{
			Name: fmt.Sprintf("%s-subnet-%d", name, i+1),
			CIDR: cidr,
		}
		if i < len(zones) {
			sp.Zone = zones[i]
		}
		subnets = append(subnets, sp)
	}
	plan.Subnets = subnets

	switch plan.Provider {
	case ProviderAWS:
		plan.ResourceType = "aws_vpc"
	case ProviderGCP:
		plan.ResourceType = "google_compute_network"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_vpc"
	case ProviderAzure:
		plan.ResourceType = "azurerm_virtual_network"
	case ProviderLinode:
		plan.ResourceType = "linode_vpc"
	case ProviderUbicloud:
		plan.ResourceType = "ubicloud_private_subnet"
	case ProviderOracle:
		plan.ResourceType = "oci_core_vcn"
	case ProviderIBM:
		plan.ResourceType = "ibm_is_vpc"
	case ProviderAlibaba:
		plan.ResourceType = "alicloud_vpc"
	case ProviderOVH:
		plan.ResourceType = "ovh_cloud_project_network_private"
	case ProviderStackIt:
		plan.ResourceType = "stackit_network"
	}
	return plan, nil
}

// deriveZones returns concrete availability zones / zones for the given number
// of subnets, derived deterministically from the concrete csp_region.
//
//   - AWS: <region><a|b|c|...> e.g. eu-west-1a, eu-west-1b — the universal AWS AZ
//     naming convention; spreads subnets multi-AZ.
//   - GCP: <region>-<a|b|c|...> e.g. europe-west1-a — GCP zone naming.
//   - DigitalOcean: VPCs are region-scoped with no sub-zones, so no zones.
//   - IBM: <region>-<1|2|3|...> e.g. eu-de-1 — IBM Cloud VPC zone naming (a
//     region has up to 3 numbered zones; the universal IBM VPC convention).
//   - Alibaba: <region><a|b|c|...> e.g. eu-central-1a — the alicloud zone-id
//     convention (same shape as AWS); vswitches are zonal, so spread multi-zone.
func deriveZones(provider, cspRegion string, n int) []string {
	if n <= 0 {
		return nil
	}
	letters := []string{"a", "b", "c", "d", "e", "f"}
	zones := make([]string, 0, n)
	switch provider {
	case ProviderAWS, ProviderAlibaba:
		for i := 0; i < n; i++ {
			zones = append(zones, cspRegion+letters[i%len(letters)])
		}
	case ProviderGCP:
		for i := 0; i < n; i++ {
			zones = append(zones, cspRegion+"-"+letters[i%len(letters)])
		}
	case ProviderDigitalOcean, ProviderLinode:
		// DigitalOcean and Linode VPCs are region-scoped with no sub-zones.
	case ProviderIBM:
		// IBM VPC zones are numbered 1..3 within a region (cycle within the 3
		// available zones if more subnets than zones are requested).
		for i := 0; i < n; i++ {
			zones = append(zones, fmt.Sprintf("%s-%d", cspRegion, (i%3)+1))
		}
	case ProviderOracle:
		// OCI availability domains carry an opaque, tenancy-specific prefix
		// (e.g. "Uocm:PHX-AD-1"), so they cannot be derived from the region name
		// the way AWS AZs / GCP zones can; the concrete AD name is only known at
		// apply time via the oci_identity_availability_domains data source. We
		// therefore carry the AD ORDINAL ("1","2","3"...) as the zone, and the
		// renderer indexes the data source by (ordinal-1). This keeps the multi-AD
		// spread deterministic and catalog-free without inventing an AD name.
		for i := 0; i < n; i++ {
			zones = append(zones, fmt.Sprintf("%d", i+1))
		}
	case ProviderStackIt:
		// StackIt availability zones are <region>-<1|2|3> e.g. eu01-1, eu01-2,
		// eu01-3 (the documented IaaS AZ naming); spread subnets across them.
		for i := 0; i < n; i++ {
			zones = append(zones, fmt.Sprintf("%s-%d", cspRegion, (i%3)+1))
		}
	}
	return zones
}

func validateSpec(spec NetworkSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("network: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("network: provider is required (aws | gcp | digitalocean | oracle)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("network: unknown provider %q (aws | gcp | digitalocean | oracle)", spec.Provider)
	}
	if strings.TrimSpace(spec.CIDR) == "" {
		return fmt.Errorf("network: cidr is required, e.g. 10.0.0.0/16")
	}
	_, vpcNet, err := net.ParseCIDR(spec.CIDR)
	if err != nil {
		return fmt.Errorf("network: invalid cidr %q: %w", spec.CIDR, err)
	}
	for _, s := range spec.Subnets {
		ip, subNet, err := net.ParseCIDR(s)
		if err != nil {
			return fmt.Errorf("network: invalid subnet cidr %q: %w", s, err)
		}
		if !vpcNet.Contains(ip) {
			return fmt.Errorf("network: subnet %q is not inside vpc cidr %q", s, spec.CIDR)
		}
		// Reject subnets wider than the VPC (a contained network start IP can
		// still have too small a prefix).
		vpcOnes, _ := vpcNet.Mask.Size()
		subOnes, _ := subNet.Mask.Size()
		if subOnes < vpcOnes {
			return fmt.Errorf("network: subnet %q is wider than vpc cidr %q", s, spec.CIDR)
		}
	}
	return nil
}
