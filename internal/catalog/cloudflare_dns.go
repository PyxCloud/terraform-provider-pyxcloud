package catalog

import (
	"fmt"
	"strings"
)

// DNSRecordSpec is one Cloudflare DNS record. Cloudflare is a DNS/edge provider
// orthogonal to the compute cloud (our repos front AWS with Cloudflare DNS), so
// this is a standalone component, NOT part of the cloud dns-zone catalog
// (Route53/CloudDNS/DO). The zone is supplied out-of-band via a Terraform
// variable (the same out-of-band pattern wave-1/OVH use for ARNs / project ids),
// so a zone id never lands in the canonical topology or state plaintext.
type DNSRecordSpec struct {
	Type    string // A | AAAA | CNAME | TXT | MX | ...
	Name    string // record name, e.g. "mcp" or "@"
	Value   string // record content
	TTL     int    // seconds; 1 = automatic (required when Proxied)
	Proxied bool   // orange-cloud (proxied through Cloudflare)
}

// CloudflareDNSSpec is the canonical Cloudflare DNS component: a set of records in
// one zone. ZoneVar is the name of the Terraform variable holding the zone id.
type CloudflareDNSSpec struct {
	Name    string
	ZoneVar string // terraform variable name carrying the Cloudflare zone id
	Records []DNSRecordSpec
}

// CloudflareDNSPlan is the deterministic concrete translation.
type CloudflareDNSPlan struct {
	Name         string          `json:"name"`
	ZoneVar      string          `json:"zone_var"`
	Records      []DNSRecordSpec `json:"records"`
	ResourceType string          `json:"resource_type"`
}

var validDNSRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "TXT": true, "MX": true, "NS": true, "SRV": true, "CAA": true,
}

// TranslateCloudflareDNS resolves a CloudflareDNSSpec into a concrete plan. There
// is no cloud provider / region here — Cloudflare is provider-agnostic DNS.
func TranslateCloudflareDNS(spec CloudflareDNSSpec) (CloudflareDNSPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return CloudflareDNSPlan{}, fmt.Errorf("cloudflare-dns: name is required")
	}
	if strings.TrimSpace(spec.ZoneVar) == "" {
		return CloudflareDNSPlan{}, fmt.Errorf("cloudflare-dns: zone_var (terraform variable holding the zone id) is required")
	}
	if len(spec.Records) == 0 {
		return CloudflareDNSPlan{}, fmt.Errorf("cloudflare-dns: declare at least one record")
	}
	for _, r := range spec.Records {
		if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Value) == "" {
			return CloudflareDNSPlan{}, fmt.Errorf("cloudflare-dns: each record needs a name and value")
		}
		if !validDNSRecordTypes[strings.ToUpper(strings.TrimSpace(r.Type))] {
			return CloudflareDNSPlan{}, fmt.Errorf("cloudflare-dns: record %q has invalid type %q", r.Name, r.Type)
		}
		if r.Proxied && r.TTL != 0 && r.TTL != 1 {
			return CloudflareDNSPlan{}, fmt.Errorf("cloudflare-dns: proxied record %q must use automatic TTL (1)", r.Name)
		}
	}
	return CloudflareDNSPlan{
		Name:         spec.Name,
		ZoneVar:      spec.ZoneVar,
		Records:      spec.Records,
		ResourceType: "cloudflare_dns_record",
	}, nil
}

// RenderCloudflareDNSHCL renders a resolved plan into cloudflare_dns_record
// resources. The zone id is referenced via the out-of-band variable.
func RenderCloudflareDNSHCL(plan CloudflareDNSPlan) (string, error) {
	var b strings.Builder
	for i, r := range plan.Records {
		rn := tfName(fmt.Sprintf("%s-%s-%d", plan.Name, r.Name, i))
		ttl := r.TTL
		if ttl <= 0 {
			ttl = 1 // automatic
		}
		fmt.Fprintf(&b, "resource \"cloudflare_dns_record\" %q {\n", rn)
		fmt.Fprintf(&b, "  zone_id = var.%s\n", plan.ZoneVar)
		fmt.Fprintf(&b, "  type    = %q\n", strings.ToUpper(r.Type))
		fmt.Fprintf(&b, "  name    = %q\n", r.Name)
		fmt.Fprintf(&b, "  content = %q\n", r.Value)
		fmt.Fprintf(&b, "  ttl     = %d\n", ttl)
		fmt.Fprintf(&b, "  proxied = %t\n", r.Proxied)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}
