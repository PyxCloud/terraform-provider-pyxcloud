package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Edge covers the three edge/network macro components (SPEC §5.8): dns-zone,
// cdn-service, and waf-service. These are GLOBAL or region-agnostic services, but
// they still resolve the region (for the catalog contract + the resolved
// csp_region surfaced in the plan, used by CDN origin / WAF association).

// ── dns-zone ─────────────────────────────────────────────────────────────────
//
//   - AWS: aws_route53_zone (a public hosted zone for the domain).
//   - GCP: google_dns_managed_zone (a public managed zone).
//   - DigitalOcean: digitalocean_domain (DO DNS).
//
// All three providers have a clean DNS-zone primitive, so there is no unsupported
// path. A zone is the authoritative container for a domain's records; individual
// records are out of scope for the macro component (they are app-level config).

// DNSZoneSpec is the abstract dns-zone description. Provider-neutral.
type DNSZoneSpec struct {
	Name     string // component name
	Region   string
	Provider string

	// Domain is the DNS name the zone is authoritative for, e.g. "example.com".
	Domain string
	// Private marks an internal/private zone (Route53 private / Cloud DNS private).
	// DO has only public domains, so Private on DO is a clean unsupported error.
	Private bool
	// Network is the place's VPC, required to scope a private zone (AWS/GCP).
	Network string
}

// DNSZonePlan is the catalog-resolved concrete dns-zone translation.
type DNSZonePlan struct {
	Provider     string `json:"provider"`
	CSP          string `json:"csp"`
	RegionName   string `json:"region_name"`
	CSPRegion    string `json:"csp_region"`
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	Private      bool   `json:"private"`
	NetworkName  string `json:"network_name"`
	ResourceType string `json:"resource_type"`
}

// TranslateDNSZone resolves a DNSZoneSpec. All three providers support a public
// zone; a PRIVATE zone is unsupported on DO (clean error).
func TranslateDNSZone(ctx context.Context, cat RegionCatalog, spec DNSZoneSpec) (DNSZonePlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return DNSZonePlan{}, fmt.Errorf("dns-zone: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Domain) == "" {
		return DNSZonePlan{}, fmt.Errorf("dns-zone: domain is required, e.g. example.com")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return DNSZonePlan{}, fmt.Errorf("dns-zone: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return DNSZonePlan{}, err
	}
	provider := lc(spec.Provider)
	if spec.Private && provider == ProviderDigitalOcean {
		return DNSZonePlan{}, ErrComponentUnsupported{
			Component: TypeDNSZone, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "DigitalOcean DNS (digitalocean_domain) is public-only; for a PRIVATE zone " +
				"use AWS Route53 private hosted zones or GCP Cloud DNS private zones",
		}
	}
	plan := DNSZonePlan{
		Provider:    provider,
		CSP:         row.CSP,
		RegionName:  row.RegionName,
		CSPRegion:   row.CSPRegion,
		Name:        canonicalName(spec.Name, "pyxcloud-zone"),
		Domain:      strings.TrimSpace(spec.Domain),
		Private:     spec.Private,
		NetworkName: spec.Network,
	}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_route53_zone"
	case ProviderGCP:
		plan.ResourceType = "google_dns_managed_zone"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_domain"
	case ProviderOracle:
		plan.ResourceType = "oci_dns_zone"
	}
	return plan, nil
}

// ── cdn-service ──────────────────────────────────────────────────────────────
//
//   - AWS: aws_cloudfront_distribution fronting an origin (the place's object
//     storage or LB DNS), HTTPS-redirect, the managed CachingOptimized policy.
//   - GCP: a google_compute_backend_bucket (or backend service) with
//     enable_cdn = true — Cloud CDN is enabled ON a backend, the GCP CDN shape.
//   - DigitalOcean: digitalocean_cdn over a Spaces origin (the only clean DO CDN
//     primitive). If no Spaces/object-storage origin is available it is a clean
//     unsupported error (DO CDN cannot front an arbitrary origin).
//
// SECURITY: CDN serves over HTTPS; viewer-protocol-policy redirects http->https.

// CDNSpec is the abstract cdn-service description. Provider-neutral.
type CDNSpec struct {
	Name     string
	Region   string
	Provider string

	// OriginKind is what the CDN fronts: "object-storage" (a bucket origin) or
	// "load-balancer" (a dynamic origin). DO CDN ONLY supports a Spaces/object
	// origin, so "load-balancer" on DO is a clean unsupported error.
	OriginKind string
	// OriginName is the canonical name of the fronted component (bucket or LB).
	OriginName string
}

// Origin kinds for a CDN.
const (
	CDNOriginObjectStorage = "object-storage"
	CDNOriginLoadBalancer  = "load-balancer"
)

// CDNPlan is the catalog-resolved concrete cdn-service translation.
type CDNPlan struct {
	Provider     string `json:"provider"`
	CSP          string `json:"csp"`
	RegionName   string `json:"region_name"`
	CSPRegion    string `json:"csp_region"`
	Name         string `json:"name"`
	OriginKind   string `json:"origin_kind"`
	OriginName   string `json:"origin_name"`
	ResourceType string `json:"resource_type"`
}

// TranslateCDN resolves a CDNSpec. AWS/GCP support any origin; DO supports only a
// Spaces/object-storage origin (a load-balancer origin on DO is a clean error).
func TranslateCDN(ctx context.Context, cat RegionCatalog, spec CDNSpec) (CDNPlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return CDNPlan{}, fmt.Errorf("cdn-service: region (abstract pyx region_name) is required")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return CDNPlan{}, fmt.Errorf("cdn-service: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	originKind := lc(spec.OriginKind)
	if originKind == "" {
		originKind = CDNOriginObjectStorage
	}
	if originKind != CDNOriginObjectStorage && originKind != CDNOriginLoadBalancer {
		return CDNPlan{}, fmt.Errorf("cdn-service: invalid origin_kind %q (object-storage | load-balancer)", spec.OriginKind)
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return CDNPlan{}, err
	}
	provider := lc(spec.Provider)
	if provider == ProviderOracle {
		// OCI has no clean, first-class CDN Terraform resource (its edge/WAF and
		// object-storage pre-authenticated/public access cover adjacent needs, but
		// there is no managed CDN-distribution resource in oracle/oci to descend
		// `cdn-service` to). Per SPEC §1/§4 we surface a clean plan-time error
		// rather than invent a resource.
		return CDNPlan{}, ErrComponentUnsupported{
			Component: TypeCDNService, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "Oracle Cloud has no first-class CDN distribution resource in the oracle/oci " +
				"Terraform provider; use AWS CloudFront or GCP Cloud CDN for the CDN tier, or front " +
				"the OCI origin with a third-party CDN (the object-storage bucket can serve public " +
				"objects directly where that suffices)",
		}
	}
	if provider == ProviderDigitalOcean && originKind != CDNOriginObjectStorage {
		return CDNPlan{}, ErrComponentUnsupported{
			Component: TypeCDNService, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "DigitalOcean CDN (digitalocean_cdn) can only front a Spaces (object-storage) " +
				"origin, not an arbitrary load-balancer origin; use AWS CloudFront or GCP Cloud CDN " +
				"for a dynamic/LB origin, or put a Spaces bucket in front",
		}
	}
	plan := CDNPlan{
		Provider:   provider,
		CSP:        row.CSP,
		RegionName: row.RegionName,
		CSPRegion:  row.CSPRegion,
		Name:       canonicalName(spec.Name, "pyxcloud-cdn"),
		OriginKind: originKind,
		OriginName: canonicalName(spec.OriginName, ""),
	}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_cloudfront_distribution"
	case ProviderGCP:
		if originKind == CDNOriginObjectStorage {
			plan.ResourceType = "google_compute_backend_bucket"
		} else {
			plan.ResourceType = "google_compute_backend_service"
		}
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_cdn"
	}
	return plan, nil
}

// ── waf-service ──────────────────────────────────────────────────────────────
//
//   - AWS: aws_wafv2_web_acl (regional or CLOUDFRONT scope) with the AWS managed
//     common-rule-set; default action ALLOW, managed rules BLOCK.
//   - GCP: google_compute_security_policy (Cloud Armor) with a default-allow rule
//     plus a preconfigured WAF expression rule.
//   - DigitalOcean: UNSUPPORTED. DO has no managed WAF primitive. Clean error.
//
// SECURITY: a WAF attaches to a front-end (LB/CDN); PyxCloud wires the managed
// common rule set by default so a fresh WAF is not a no-op.

// WAFSpec is the abstract waf-service description. Provider-neutral.
type WAFSpec struct {
	Name     string
	Region   string
	Provider string

	// Scope is "regional" (front a regional ALB/Cloud Armor) or "cloudfront"
	// (AWS global, front a CloudFront distribution). GCP is always regional-ish.
	Scope string
	// AssociateName is the canonical name of the LB/CDN the WAF protects (optional).
	AssociateName string
}

// WAF scopes.
const (
	WAFScopeRegional   = "regional"
	WAFScopeCloudFront = "cloudfront"
)

// WAFPlan is the catalog-resolved concrete waf-service translation.
type WAFPlan struct {
	Provider      string `json:"provider"`
	CSP           string `json:"csp"`
	RegionName    string `json:"region_name"`
	CSPRegion     string `json:"csp_region"`
	Name          string `json:"name"`
	Scope         string `json:"scope"`
	AssociateName string `json:"associate_name"`
	ResourceType  string `json:"resource_type"`
}

// TranslateWAF resolves a WAFSpec. DO is a clean unsupported error.
func TranslateWAF(ctx context.Context, cat RegionCatalog, spec WAFSpec) (WAFPlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return WAFPlan{}, fmt.Errorf("waf-service: region (abstract pyx region_name) is required")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return WAFPlan{}, fmt.Errorf("waf-service: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	scope := lc(spec.Scope)
	if scope == "" {
		scope = WAFScopeRegional
	}
	if scope != WAFScopeRegional && scope != WAFScopeCloudFront {
		return WAFPlan{}, fmt.Errorf("waf-service: invalid scope %q (regional | cloudfront)", spec.Scope)
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return WAFPlan{}, err
	}
	provider := lc(spec.Provider)
	if provider == ProviderDigitalOcean {
		return WAFPlan{}, ErrComponentUnsupported{
			Component: TypeWAFService, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "DigitalOcean has no managed WAF primitive; use AWS WAFv2 or GCP Cloud Armor, " +
				"or front the app with a self-managed WAF (ModSecurity/Coraza) on a virtual-machine",
		}
	}
	if provider == ProviderGCP && scope == WAFScopeCloudFront {
		return WAFPlan{}, ErrComponentUnsupported{
			Component: TypeWAFService, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "the cloudfront WAF scope is AWS-specific; on GCP use the default (regional) " +
				"Cloud Armor policy attached to a backend service",
		}
	}
	if provider == ProviderOracle && scope == WAFScopeCloudFront {
		return WAFPlan{}, ErrComponentUnsupported{
			Component: TypeWAFService, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "the cloudfront WAF scope is AWS-specific; on Oracle Cloud the WAF " +
				"(oci_waf_web_app_firewall) attaches to a load balancer — use the default (regional) scope",
		}
	}
	plan := WAFPlan{
		Provider:      provider,
		CSP:           row.CSP,
		RegionName:    row.RegionName,
		CSPRegion:     row.CSPRegion,
		Name:          canonicalName(spec.Name, "pyxcloud-waf"),
		Scope:         scope,
		AssociateName: canonicalName(spec.AssociateName, ""),
	}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_wafv2_web_acl"
	case ProviderGCP:
		plan.ResourceType = "google_compute_security_policy"
	case ProviderOracle:
		plan.ResourceType = "oci_waf_web_app_firewall"
	}
	return plan, nil
}

// CanonicalDNSZoneType / CanonicalCDNType / CanonicalWAFType report whether t
// names the respective component.
func CanonicalDNSZoneType(t string) (string, bool) {
	if lc(t) == TypeDNSZone {
		return TypeDNSZone, true
	}
	return "", false
}

func CanonicalCDNType(t string) (string, bool) {
	if lc(t) == TypeCDNService {
		return TypeCDNService, true
	}
	return "", false
}

func CanonicalWAFType(t string) (string, bool) {
	if lc(t) == TypeWAFService {
		return TypeWAFService, true
	}
	return "", false
}
