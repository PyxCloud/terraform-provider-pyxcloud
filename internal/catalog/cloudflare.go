package catalog

import (
	"context"
	"fmt"
	"strings"
)

// CloudflareDNS is the abstract `dns` component for Cloudflare — the canonical
// form of the per-provider scripts' cloudflare_dns_record glue. It is CROSS-CUTTING:
// Cloudflare is an edge/DNS provider used ALONGSIDE the cloud provider (an AWS env
// with Cloudflare DNS), so it renders cloudflare_* resources regardless of the
// environment's cloud provider, and requires the cloudflare/cloudflare terraform
// provider (auth via the CLOUDFLARE_API_TOKEN env var — the same way our scripts
// authenticate today).
//
// This is distinct from the catalog `dns-zone` component, which is cloud-native
// DNS (Route53 / Cloud DNS / DO). Our repos use Cloudflare, hence this component.

// DNSRecord is one Cloudflare DNS record.
type DNSRecord struct {
	Name    string // record name, e.g. "api" or "api.example.com"
	Type    string // A | AAAA | CNAME | TXT | MX | ...
	Content string // the record value (IP, target host, text)
	TTL     int    // seconds; 1 = automatic
	Proxied bool   // orange-cloud (proxied through Cloudflare)
}

// CloudflareDNSSpec is the abstract Cloudflare DNS description. ZoneID is supplied
// out of band (a var / the CLOUDFLARE_ZONE_ID env), never invented.
type CloudflareDNSSpec struct {
	Name    string
	ZoneID  string
	Records []DNSRecord
}

// CloudflareDNSPlan is the resolved concrete plan.
type CloudflareDNSPlan struct {
	Name         string      `json:"name"`
	ZoneID       string      `json:"zone_id"`
	Records      []DNSRecord `json:"records"`
	ResourceType string      `json:"resource_type"`
}

var dnsRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "TXT": true, "MX": true, "NS": true,
	"SRV": true, "CAA": true, "PTR": true,
}

// TranslateCloudflareDNS validates and resolves a CloudflareDNSSpec. Provider-
// independent (Cloudflare is global), so no region/cloud-provider resolution.
func TranslateCloudflareDNS(_ context.Context, spec CloudflareDNSSpec) (CloudflareDNSPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return CloudflareDNSPlan{}, fmt.Errorf("dns: name is required")
	}
	if len(spec.Records) == 0 {
		return CloudflareDNSPlan{}, fmt.Errorf("dns: at least one record is required")
	}
	for i, r := range spec.Records {
		if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Content) == "" {
			return CloudflareDNSPlan{}, fmt.Errorf("dns: record %d needs a name and content", i+1)
		}
		if !dnsRecordTypes[strings.ToUpper(strings.TrimSpace(r.Type))] {
			return CloudflareDNSPlan{}, fmt.Errorf("dns: record %q has unsupported type %q", r.Name, r.Type)
		}
	}
	return CloudflareDNSPlan{
		Name: spec.Name, ZoneID: spec.ZoneID, Records: spec.Records,
		ResourceType: "cloudflare_dns_record",
	}, nil
}

// RenderCloudflareDNSHCL renders a CloudflareDNSPlan into cloudflare_dns_record
// resources. zone_id falls back to a var so it is supplied out of band.
func RenderCloudflareDNSHCL(p CloudflareDNSPlan) (string, error) {
	var b strings.Builder
	zoneRef := fmt.Sprintf("%q", p.ZoneID)
	if strings.TrimSpace(p.ZoneID) == "" {
		zoneRef = "var.cloudflare_zone_id"
		b.WriteString("variable \"cloudflare_zone_id\" {\n  type = string\n}\n\n")
	}
	for i, r := range p.Records {
		rn := tfName(fmt.Sprintf("%s-%d", p.Name, i+1))
		fmt.Fprintf(&b, "resource \"cloudflare_dns_record\" %q {\n", rn)
		fmt.Fprintf(&b, "  zone_id = %s\n", zoneRef)
		fmt.Fprintf(&b, "  name    = %q\n", r.Name)
		fmt.Fprintf(&b, "  type    = %q\n", strings.ToUpper(r.Type))
		fmt.Fprintf(&b, "  content = %q\n", r.Content)
		ttl := r.TTL
		if ttl <= 0 {
			ttl = 1 // automatic
		}
		fmt.Fprintf(&b, "  ttl     = %d\n", ttl)
		fmt.Fprintf(&b, "  proxied = %t\n", r.Proxied)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

// cloudflareRequiredProviders is the terraform block that pins the Cloudflare
// provider source — emitted by AssembleHCL when any Cloudflare component is present
// (terraform would otherwise assume the non-existent hashicorp/cloudflare).
const cloudflareRequiredProviders = `terraform {
  required_providers {
    cloudflare = {
      source = "cloudflare/cloudflare"
    }
  }
}
`
